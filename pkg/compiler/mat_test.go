package compiler

import (
	"testing"

	//"github.com/gijit/gi/pkg/verb"
	cv "github.com/glycerine/goconvey/convey"
)

func Test500MatrixDeclOfDoubleSlice(t *testing.T) {

	cv.Convey(`[][]float inside matrix struct`, t, func() {

		src := `
type Matrix struct {
	A    [][]float64
	Nrow int
	Ncol int
}
m := &Matrix{A:[][]float64{[]float64{1,2}}}
e := m.A[0][1]
`
		// e == 2
		vm, err := NewLuaVmWithPrelude(nil)
		panicOn(err)
		defer vm.Close()
		inc := NewIncrState(vm, nil)

		translation := inc.Tr([]byte(src))
		pp("go:'%s'  -->  '%s' in lua\n", src, string(translation))
		//fmt.Printf("go:'%#v'  -->  '%#v' in lua\n", src, translation)

		cv.So(string(translation), matchesLuaSrc, `
	__type__Matrix = __newType(0, __kindStruct, "main.Matrix", true, "main", true, nil);
  	
  	__type__anon_sliceType = __sliceType(__type__anon_sliceType); 
  
  	
  	__type__anon_sliceType = __sliceType(__type__float64); 
  
  	__type__Matrix.init("", {{__prop= "A", __name= "A", __anonymous= false, __exported= true, __typ= __type__anon_sliceType, __tag= ""}, {__prop= "Nrow", __name= "Nrow", __anonymous= false, __exported= true, __typ= __type__int, __tag= ""}, {__prop= "Ncol", __name= "Ncol", __anonymous= false, __exported= true, __typ= __type__int, __tag= ""}}); 
  	
  	 __type__Matrix.__constructor = function(self, ...) 
  		 if self == nil then self = {}; end
  			 local A_, Nrow_, Ncol_ = ... ;
  			 self.A = A_ or __type__anon_sliceType.__nil;
  			 self.Nrow = Nrow_ or 0LL;
  			 self.Ncol = Ncol_ or 0LL;
  		 return self; 
  	 end;
  ;
  
  	m = __type__Matrix.ptr({}, __type__anon_sliceType({[0]=__type__anon_sliceType({[0]=1, 2})}), 0LL, 0LL);
  	e = __gi_GetRangeCheck(__gi_GetRangeCheck(m.A, 0), 1);
`)

		LoadAndRunTestHelper(t, vm, translation)

		LuaMustFloat64(vm, "e", 2)
	})
}