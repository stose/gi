#define LUA_LIB
#include "lj_obj.h"

#if LJ_HASFFI

#include "lj_state.h"
#include "lj_gc.h"
#include "lj_err.h"
#include "lj_tab.h"
#include "lj_ctype.h"
#include "lj_cconv.h"
#include "lj_cdata.h"
#include "lauxlib.h"
#include <strings.h> /*memset*/

/* compilers have different ideas about how to print 64-bit ints*/
#if LUAJIT_OS==LUAJIT_OS_WINDOWS
#define RETURN_INT64_FORMAT  "return %I64dLL"
#define RETURN_UINT64_FORMAT "return %I64uLL"
#else
#if LJ_TARGET_OSX
#define RETURN_INT64_FORMAT  "return %lldLL"
#define RETURN_UINT64_FORMAT "return %lluLL"
#else
#define RETURN_INT64_FORMAT  "return %ldLL"
#define RETURN_UINT64_FORMAT "return %luLL"
#endif
#endif

LUA_API uint32_t
luajit_push_cdata_int64(struct lua_State *L, int64_t n)
{
  int idx = lua_gettop(L);
  /* load cdata int64 returning function onto stack */
  char buf[128];
  memset(&buf[0], '\0', 128);
  snprintf(&buf[0], 127, RETURN_INT64_FORMAT, n);
  
  int err = luaL_loadstring(L, &buf[0]);
  if (err != 0) {
    return luaL_error(L, "luajit_push_cdata_int64 error: could not loadstring");
  }

  err = lua_pcall(L, 0, 1, 0);
  if (err != 0) {
    lua_settop(L, idx);
    return luaL_error(L, "luajit_push_cdata_int64 error: pcall to load cdata onto stack failed.");
  }
  return 0;
}


LUA_API uint32_t
luajit_push_cdata_uint64(struct lua_State *L, uint64_t u)
{
  int idx = lua_gettop(L);
  /* load cdata int64 returning function onto stack */
  char buf[128];
  memset(&buf[0], '\0', 128);
  snprintf(&buf[0], 127, RETURN_UINT64_FORMAT, u);
  
  int err = luaL_loadstring(L, &buf[0]);
  if (err != 0) {
    return luaL_error(L, "luajit_push_cdata_uint64 error: could not loadstring");
  }

  err = lua_pcall(L, 0, 1, 0);
  if (err != 0) {
    lua_settop(L, idx);
    return luaL_error(L, "luajit_push_cdata_uint64 error: pcall to load cdata onto stack failed.");
  }
  return 0;
}


LUA_API uint32_t
luajit_ctypeid(struct lua_State *L, int idxNew)
{
  //printf("top of luajit_ctypeid, L=%p, idxNew=%d, top=%d\n", L, idxNew, lua_gettop(L));
  int restoreAtEndIdx = lua_gettop(L);
  if (restoreAtEndIdx == 0) {
      return luaL_error(L, "luajit-ffi-ctypeid: empty stack.");
  }
  
  /* convert from relative to absolute index,
     but try to avoid changing any pseudo indexes,
     which are large negative ints.
  */
  if (idxNew < 0 && (-idxNew) <= restoreAtEndIdx) {
    idxNew = (restoreAtEndIdx + 1) + idxNew;
  }
  
  CTypeID ctypeid;
  GCcdata *cd;

  /* Get ref to ffi.typeof */
  int err = luaL_loadstring(L, "return require('ffi').typeof");
  if (err != 0) {
    lua_settop(L, restoreAtEndIdx);
    return luaL_error(L, "luajit-ffi-ctypeid error: could not loadstring");
  }

  //printf("debug luajit_ctypeid, just before lua_pcall to ffi.typeof\n");  
  err = lua_pcall(L, 0, 1, 0);
  //printf("debug luajit_ctypeid, just after lua_pcall to ffi.typeof\n");    
  if (err != 0) {
    lua_settop(L, restoreAtEndIdx);
    return luaL_error(L, "luajit-ffi-ctypeid pcall to require ffi.typeof failed.");
  }
  
  if (!lua_isfunction(L, -1)) {
    int new_top = lua_gettop(L);
    lua_settop(L, restoreAtEndIdx);
    return luaL_error(L, "luajit-ffi-ctypeid: !lua_isfunction() at top of stack; new_top=%d", new_top);
  }
  /* Push the first argument to ffi.typeof */
  lua_pushvalue(L, idxNew);
  /* Call ffi.typeof() */

  //printf("debug luajit_ctypeid, just before 2nd pcall\n");      
  err = lua_pcall(L, 1, 1, 0);
  //printf("debug luajit_ctypeid, just after 2nd pcall\n"); // fine to here, crash after
  if (err != 0) {
    lua_settop(L, restoreAtEndIdx);
    /*e.g. bad argument #1 to 'typeof' (C type expected, got number)*/
    return 0; /*zero will mean we couldn't get the type b/c it wasn't a ctype*/
  }
  
  /* Returned type should be LUA_TCDATA with CTID_CTYPEID */
  if (lua_type(L, -1) != LUA_TCDATA) {
    lua_settop(L, restoreAtEndIdx);
    return luaL_error(L, "luajit-ffi-ctypeid call to ffi.typeof failed at lua_type(L,1) != LUA_TCDATA");
  }
  /*cd = cdataV(L->base);*/
  //printf("debug luajit_ctypeid, before cdataV(L->top)\n");
  //printf("debug (L->top) is \n");
  cd = cdataV(L->top);
  //cd = cdataV(L->base);
  //printf("debug luajit_ctypeid, after cdataV(L->top)\n");
  //printf("debug luajit_ctypeid, cd = %p\n", cd); // cd = 0x2d
  // this next ->ctypeid is crashing b/c cd is not really
  // a pointer.
  if (cd->ctypeid != CTID_CTYPEID) {
    //printf("in err, CTID_CTYPEID=%d, but cd->typeid=%d, about to restoreAtEndIdx=%d\n",    CTID_CTYPEID, cd->ctypeid, restoreAtEndIdx);
    lua_settop(L, restoreAtEndIdx);
    //printf("done with restoreAtEndIdx\n");  
    return luaL_error(L, "luajit-ffi-ctypeid call to ffi.typeof failed at ctypeid != CTID_CTYPEID");
  }
  //printf("debug luajit_ctypeid, before cdataptr(cd) call\n");  
  ctypeid = *(CTypeID *)cdataptr(cd);
  //printf("debug luajit_ctypeid, before settop\n");    
  lua_settop(L, restoreAtEndIdx);
  //printf("debug luajit_ctypeid, after settop\n");  
  return ctypeid;
}

#endif
