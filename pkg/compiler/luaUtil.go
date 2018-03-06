package compiler

import (
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/gijit/gi/pkg/verb"
	golua "github.com/glycerine/golua/lua"
	"github.com/glycerine/luar"
)

// shortcut; do
// *dbg = true
// to turn on -vv very verbose debug printing.
var dbg = &verb.VerboseVerbose

var nyc *time.Location

func NewLuaVmWithPrelude(cfg *GIConfig) (*golua.State, error) {
	var vm *golua.State
	var useStaticPrelude bool

	// cfg == nil means under test.
	// cfg.Dev means `gi -d` was invoked.
	if cfg == nil || cfg.Dev {
		useStaticPrelude = false
	} else {
		useStaticPrelude = true
	}

	if cfg == nil {
		cfg = NewGIConfig()
	}

	if useStaticPrelude {
		cfg.PreludePath = ""
	} else {
		cwd, err := os.Getwd()
		panicOn(err)
		cfg.PreludePath = cwd + "/prelude"
	}

	if cfg.NoPrelude || cfg.NoLuar {
		fmt.Printf("loading LuaJIT vm without Luar.\n")
		vm = golua.NewState()
		vm.OpenLibs()
		return vm, nil
	}

	vm = luar.Init() // does vm.OpenLibs() for us, adds luar. functions.
	registerLuarReqs(vm)

	// establish prelude location so prelude can know itself.
	// __preludePath must be terminated with a '/' character.
	err := LuaRun(vm, fmt.Sprintf(`__preludePath="/";`), false) //, makePathWindowsSafe(cfg.PreludePath)), false)
	if err != nil {
		return nil, err
	}

	// load prelude
	var files []string
	if useStaticPrelude {
		if !cfg.Quiet {
			fmt.Printf("Using static prelude.\n")
		}
		// static version, compiled into prelude_static.go

		pre, err := preludeFiles.Open("")
		if err != nil {
			return nil, err
		}
		slcFileInfo, err := pre.Readdir(-1)
		if err != nil {
			return nil, err
		}
		for i, fi := range slcFileInfo {
			_ = i
			nm := fi.Name()
			// also load timezone, for windows
			if nm == "zoneinfo" {
				//fmt.Printf("loading zoneinfo/\n")
				f, err := preludeFiles.Open("zoneinfo/America/New_York")
				panicOn(err)
				nyctzdata, err := ioutil.ReadAll(f)
				panicOn(err)
				nyc, err = time.LoadLocationFromTZData("America/New_York", nyctzdata)
				panicOn(err)
				//fmt.Printf("nyc is '%s'\n", nyc)
			} else {
				if !fi.IsDir() && fi.Size() > 0 && strings.HasSuffix(nm, ".lua") {
					if !strings.HasSuffix(nm, "_test.lua") {
						//fmt.Printf("file info[%v]: '%#v'\n", i, nm)
						files = append(files, nm)
					}
				}
			}
		}

		// get a consistent application order, by sorting by name.
		sort.Strings(files)
		for _, fn := range files {

			f, err := preludeFiles.Open(fn)
			panicOn(err)

			by, err := ioutil.ReadAll(f)
			panicOn(err)

			//fmt.Printf("\n--88-- ioutil.ReadAll('%s') returned:\n'%s'\n", fn, string(by))
			err = LuaRun(vm, string(by), false)
			if err != nil {
				//fmt.Printf("problem loading prelude file '%s': '%v'\n", fn, err)
				return nil, err
			}
			//fmt.Printf("ok load of prelude file '%s'.\n", fn)
		}

	} else {
		if !cfg.Quiet {
			fmt.Printf("Dynamic prelude '%s'\n", cfg.PreludePath)
		}
		//fmt.Printf("cfg = '%#v'\n", cfg)
		files, err = FetchPreludeFilenames(cfg.PreludePath, cfg.Quiet)
		panicOn(err)
		if err != nil {
			return nil, err
		}

		err = LuaDoPreludeFiles(vm, files)
		panicOn(err)
		if err != nil {
			return nil, err
		}
	}

	// lastly, after the prelude, reset the DFS graph
	// so new type dependencies are tracked
	err = LuaRun(vm, "__dfsGlobal:reset();", false)
	if err != nil {
		return nil, err
	}

	// take a Lua value, turn it into a Go value, wrap
	// it in a proxy and return it to Lua.
	lua2GoProxy := func(b interface{}) (a interface{}) {
		return b
	}

	luar.Register(vm, "", luar.Map{
		"__lua2go": lua2GoProxy,
	})
	//fmt.Printf("registered __lua2go with luar.\n")

	return vm, err
}

func LuaDoPreludeFiles(vm *golua.State, files []string) error {
	for _, f := range files {
		pp("LuaDoFiles, f = '%s'", f)
		err := LuaRun(vm, fmt.Sprintf(`dofile("%s")`, f), false)
		if err != nil {
			return err
		}
	}
	return nil
}

// user files, those after the prelude load, get run
// on the main eval coroutine, so they can call goroutines,
// channels, etc.
func LuaDoUserFiles(vm *golua.State, files []string) error {
	for _, f := range files {
		pp("LuaDoUserFiles, f = '%s'", f)
		err := LuaRun(vm, fmt.Sprintf(`dofile("%s")`, f), true)
		if err != nil {
			return err
		}
	}
	return nil
}

func DumpLuaStack(L *golua.State) {
	fmt.Printf("\n%s\n", DumpLuaStackAsString(L, 0))
}

func DumpLuaStackAsString(L *golua.State, ignoreTop int) (s string) {
	var top int

	top = L.GetTop()
	isMain := L.PushThread()
	thr := L.ToThread(-1)

	// cleanup stack
	L.SetTop(top)

	s += fmt.Sprintf("== begin DumpLuaStack (of coro %p/lua.State=%p; isMain=%v): top = %v\n", thr, thr.S, isMain, top-ignoreTop)
	for i := top - ignoreTop; i >= 1; i-- {

		t := L.Type(i)
		s += fmt.Sprintf("DumpLuaStack: i=%v, t= %v\n", i, t)
		s += golua.LuaStackPosToString(L, i)
	}
	s += fmt.Sprintf("== end of DumpLuaStack\n")
	return
}

func FetchPreludeFilenames(preludePath string, quiet bool) ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	pp("FetchPrelude called on path '%s', where cwd = '%s'", preludePath, cwd)
	if !DirExists(preludePath) {
		return nil, fmt.Errorf("-prelude dir does not exist: '%s'", preludePath)
	}
	files, err := filepath.Glob(fmt.Sprintf("%s/*.lua", preludePath))
	if err != nil {
		return nil, fmt.Errorf("-prelude dir '%s' open problem: '%v'", preludePath, err)
	}
	if len(files) < 1 {
		return nil, fmt.Errorf("-prelude dir '%s' had no lua files in it.", preludePath)
	}
	// filter out *test.lua
	keepers := []string{}
	for _, fn := range files {
		if !strings.HasSuffix(fn, "test.lua") {
			keepers = append(keepers, fn)
		}
	}
	files = keepers
	// get a consistent application order, by sorting by name.
	sort.Strings(files)
	if !quiet {
		//fmt.Printf("\nloading prelude: '%s'\n", preludePath)
		shortFn := make([]string, len(files))
		for i, fn := range files {
			shortFn[i] = filepath.Base(fn)
		}
		pp("using these files as prelude: %s\n", strings.Join(shortFn, ", "))
	}
	// windows needs the \ turned into \\ in order to work
	if runtime.GOOS == "windows" {
		for i := range files {
			files[i] = makePathWindowsSafe(files[i])
		}
	}
	return files, nil
}

func makePathWindowsSafe(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	return strings.Replace(path, `\`, `\\`, -1)
}

// prefer below LuaMustInt64
func LuaMustInt(vm *golua.State, varname string, expect int) {

	vm.GetGlobal(varname)
	top := vm.GetTop()
	value_int := vm.ToInteger(top) // lossy for 64-bit int64, use vm.CdataToInt64() instead.

	pp("LuaMustInt, expect='%v'; observe value_int='%v'", expect, value_int)
	if value_int != expect {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected %v, got %v for '%v'", expect, value_int, varname))
	}
}

func LuaMustInt64(vm *golua.State, varname string, expect int64) {

	vm.GetGlobal(varname)
	top := vm.GetTop()
	if vm.IsNil(top) {
		panic(fmt.Sprintf("global variable '%s' is nil", varname))
	}
	value_int := vm.CdataToInt64(top)

	pp("LuaMustInt64, expect='%v'; observe value_int='%v'", expect, value_int)
	if value_int != expect {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected %v, got %v for '%v'", expect, value_int, varname))
	}
	vm.Pop(1)
}

func LuaMustEvalToInt64(vm *golua.State, xpr string, expect int64) {

	evalme := "__tmp = " + xpr
	fmt.Printf("evalme = '%s'\n", evalme)
	LuaRun(vm, evalme, true)
	vm.GetGlobal("__tmp")
	top := vm.GetTop()
	if vm.IsNil(top) {
		panic(fmt.Sprintf("global variable '__tmp' is nil, after running: '%s'", evalme))
	}
	value_int := vm.CdataToInt64(top)

	pp("LuaMustEvalToInt64, expect='%v'; observe value_int='%v'", expect, value_int)
	if value_int != expect {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected %v, got %v for '%v'", expect, value_int, evalme))
	}
	vm.Pop(1)
}

func LuaInGlobalEnv(vm *golua.State, varname string) bool {

	vm.GetGlobal(varname)
	ret := !vm.IsNil(-1)
	vm.Pop(1)
	return ret
}

func LuaMustNotBeInGlobalEnv(vm *golua.State, varname string) {

	if LuaInGlobalEnv(vm, varname) {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected %v to not be in global env, but it was.", varname))
	}
}

func LuaMustBeInGlobalEnv(vm *golua.State, varname string) {

	if !LuaInGlobalEnv(vm, varname) {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected %v to be in global env, but it was not.", varname))
	}
}

func LuaMustFloat64(vm *golua.State, varname string, expect float64) {

	vm.GetGlobal(varname)
	top := vm.GetTop()
	value := vm.ToNumber(top)

	pp("LuaMustInt64, expect='%v'; observed value='%v'", expect, value)
	if math.Abs(value-expect) > 1e-8 {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected %v, got %v for '%v'", expect, value, varname))
	}
	vm.Pop(1)
}

func LuaMustString(vm *golua.State, varname string, expect string) {

	vm.GetGlobal(varname)
	top := vm.GetTop()
	value_string := vm.ToString(top)

	pp("value_string=%v", value_string)
	if value_string != expect {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected %v, got value '%s' -> '%v'", expect, varname, value_string))
	}
	vm.Pop(1)
}

func LuaMustBool(vm *golua.State, varname string, expect bool) {

	vm.GetGlobal(varname)
	top := vm.GetTop()
	value_bool := vm.ToBoolean(top)

	pp("value_bool=%v", value_bool)
	if value_bool != expect {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected %v, got value '%s' -> '%v'", expect, varname, value_bool))
	}
	vm.Pop(1)
}

func LuaMustBeNil(vm *golua.State, varname string) {
	isNil, alt := LuaIsNil(vm, varname)

	if !isNil {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected varname '%s' to "+
			"be nil, but was '%s' instead.", varname, alt))
	}
	vm.Pop(1)
}
func LuaIsNil(vm *golua.State, varname string) (bool, string) {

	vm.GetGlobal(varname)
	isNil := vm.IsNil(-1)
	top := vm.GetTop()
	vm.Pop(1)
	return isNil, golua.LuaStackPosToString(vm, top)
}

func LuaRunAndReport(vm *golua.State, s string) {
	err := LuaRun(vm, s, true)

	if err != nil {
		fmt.Printf("error from LuaRun: '%v'. supplied lua with: '%s'\n",
			err, s)
		panic(err)
	}
}

type LuaRunner struct {
	vm         *golua.State
	evalThread *golua.State
}

func NewLuaRunner(vm *golua.State) *LuaRunner {
	lr := &LuaRunner{vm: vm}

	/* now we do a new coroutine per eval, so we can eval blocking actions
	       like a receive on an unbuffered channel

		vm.GetGlobal("__gijitMainCoro")
		if vm.IsNil(-1) {
			panic("could not locate __gijitMainCoro in _G: tsys.lua must have been sourced.")
		}
		lr.evalThread = vm.ToThread(-1)
		//fmt.Printf("\n ... evalThread stack is:\n'%s'\n", DumpLuaStackAsString(lr.evalThread))
		vm.Pop(1)
	*/
	return lr
}

func (lr *LuaRunner) Run(s string, useEvalCoroutine bool) error {
	return LuaRun(lr.vm, s, useEvalCoroutine)
}

// useEvalCoroutine may need to be false to bootstrap, but
// should be typically true once the prelude / __gijitMainCoro is loaded.
func LuaRun(vm *golua.State, s string, useEvalCoroutine bool) error {
	pp("LuaRun top. s='%s'. stack='%s'", s, string(debug.Stack()))
	startTop := vm.GetTop()
	defer vm.SetTop(startTop)

	if useEvalCoroutine {
		// get the eval function. it will spawn us a new coroutine
		// for each evaluation.

		vm.GetGlobal("__eval")
		if vm.IsNil(-1) {
			panic("could not locate __eval in _G")
		}
		eval := vm.ToPointer(-1)
		vm.PushString(s)

		p1("good: found __eval (0x%x). it is at -2 of the stack, our running code at -1. running '%s'\n", eval, s)
		p1("before vm.Call(1,0), stacks are:")
		if verb.Verbose {
			showLuaStacks(vm)
		}
		vm.Call(1, 0)
		// if things crash, this is the first place
		// to check for an error: dump the Lua stack.
		// With high probability, it will yield clues to the problem.
		p1("after vm.Call(1,0), stacks are:")
		if verb.Verbose {
			showLuaStacks(vm)
		}
		return nil
	} else {

		// not using the __eval coroutine.

		interr := vm.LoadString(s)
		if interr != 0 {
			loadErr := fmt.Errorf("%s", DumpLuaStackAsString(vm, 0))
			return loadErr
		} else {
			err := vm.Call(0, 0)
			if err != nil {
				runErr := fmt.Errorf("%s", DumpLuaStackAsString(vm, 0))
				return runErr
			}
		}
	}
	return nil
}

func dumpTableString(L *golua.State, index int) (s string) {

	// Push another reference to the table on top of the stack (so we know
	// where it is, and this function can work for negative, positive and
	// pseudo indices
	L.PushValue(index)
	// stack now contains: -1 => table
	L.PushNil()
	// stack now contains: -1 => nil; -2 => table
	for L.Next(-2) != 0 {

		// stack now contains: -1 => value; -2 => key; -3 => table
		// copy the key so that lua_tostring does not modify the original
		L.PushValue(-2)
		// stack now contains: -1 => key; -2 => value; -3 => key; -4 => table
		key := L.ToString(-1)
		value := L.ToString(-2)
		s += fmt.Sprintf("'%s' => '%s'\n", key, value)
		// pop value + copy of key, leaving original key
		L.Pop(2)
		// stack now contains: -1 => key; -2 => table
	}
	// stack now contains: -1 => table (when lua_next returns 0 it pops the key
	// but does not push anything.)
	// Pop table
	L.Pop(1)
	// Stack is now the same as it was on entry to this function
	return
}

func LuaMustRune(vm *golua.State, varname string, expect rune) {

	vm.GetGlobal(varname)
	top := vm.GetTop()
	value_int := rune(vm.CdataToInt64(top))

	pp("LuaMustRune, expect='%v'; observe value_int='%v'", expect, value_int)
	if value_int != expect {
		DumpLuaStack(vm)
		panic(fmt.Sprintf("expected %v, got %v for '%v'", expect, value_int, varname))
	}
}

func sumSliceOfInts(a []interface{}) (tot int) {
	for _, v := range a {
		switch y := v.(type) {
		case int:
			tot += y
		case int64:
			tot += int(y)
		case float64:
			tot += int(y)
		default:
			panic(fmt.Sprintf("unknown type '%T'", v))
		}
	}
	return
}

// for Test080
func sumArrayInt64(a [3]int64) (tot int64) {
	for i, v := range a {
		fmt.Printf("\n %v, sumArrayInt64 adding '%v' to tot", i, v)
		tot += v
	}
	fmt.Printf("\n sumArrayInt64 is returning tot='%v'", tot)
	return
}

//func __subslice(t, low, hi, cap) {
//
//}

// Lookup and return a channel (either wrapped in a table or Userdata directly)
// from _G and return it as an interface{}.
// If successful and leaveOnTop is true, we leave the channel on the top of the stack.
// Do vm.Pop(1) to clean it up. On failure, or if leaveOnTop is false, we
// leave the stack clean/as it found it.
//
func getChannelFromGlobal(vm *golua.State, varname string, leaveOnTop bool) (interface{}, error) {
	vm.GetGlobal(varname)
	top := vm.GetTop()
	if vm.IsNil(top) {
		vm.Pop(1)
		return nil, fmt.Errorf("global variable '%s' is nil", varname)
	}
	// is it a table or a cdata. if table, look for t.__native
	// to get the actual Go channel.

	t := vm.Type(top)
	switch t {
	case golua.LUA_TTABLE:
		vm.GetField(top, "__native")
		if vm.IsNil(-1) {
			vm.Pop(1)
			return nil, fmt.Errorf("no __native field, table on '%s' was not a table-wrapped channel", varname)
		}
		// okay. cleanup.
		vm.Remove(-2)
	case golua.LUA_TUSERDATA:
		// okay
	default:
		return nil, fmt.Errorf("expected table-enclosed Go channel or direct USERDATA with channel pointer; global varname '%s' was neither", varname)
	}

	top = vm.GetTop()
	var i interface{}
	_, err := luar.LuaToGo(vm, top, &i)
	if err != nil {
		return nil, err
	}

	if !leaveOnTop {
		// cleanup
		vm.Pop(1)
	}
	return (*i.(*reflect.Value)).Interface(), nil
}
