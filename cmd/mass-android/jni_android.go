//go:build android && cgo

package main

/*
#cgo android LDFLAGS: -llog
#include <stdlib.h>
#include <android/log.h>

static void massAndroidLog(const char* msg) {
	__android_log_write(ANDROID_LOG_INFO, "MassGo", msg);
}
*/
import "C"
import "unsafe"

func init() {
	defaultManager = newServerManager(func(line string) {
		cstr := C.CString(line)
		C.massAndroidLog(cstr)
		C.free(unsafe.Pointer(cstr))
	})
}

//export MassStart
func MassStart(reqJSON *C.char) *C.char {
	return C.CString(defaultManager.Start(C.GoString(reqJSON)))
}

//export MassStop
func MassStop() *C.char {
	return C.CString(defaultManager.Stop())
}

//export MassStatus
func MassStatus() *C.char {
	return C.CString(defaultManager.Status())
}
