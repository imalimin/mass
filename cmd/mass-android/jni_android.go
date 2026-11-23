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
import (
	"os"
	"strings"
	"unsafe"
)

func init() {
	if os.Getenv("SSL_CERT_DIR") == "" {
		var dirs []string
		for _, dir := range []string{
			"/apex/com.android.conscrypt/cacerts",
			"/system/etc/security/cacerts",
		} {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				dirs = append(dirs, dir)
			}
		}
		if len(dirs) > 0 {
			_ = os.Setenv("SSL_CERT_DIR", strings.Join(dirs, ":"))
		}
	}
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
