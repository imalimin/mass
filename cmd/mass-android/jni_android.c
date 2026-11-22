//go:build android && cgo

#include <jni.h>
#include <stdlib.h>
#include "_cgo_export.h"

static jstring to_jstring_and_free(JNIEnv *env, char *cstr) {
    if (cstr == NULL) {
        return (*env)->NewStringUTF(env, "{\"ok\":false,\"state\":\"error\",\"error\":\"null response\"}");
    }
    jstring result = (*env)->NewStringUTF(env, cstr);
    free(cstr);
    return result;
}

JNIEXPORT jstring JNICALL
Java_com_afinn_mass_MassBridge_nativeStart(JNIEnv *env, jobject thiz, jstring reqJson) {
    (void)thiz;
    const char *raw = (*env)->GetStringUTFChars(env, reqJson, NULL);
    if (raw == NULL) {
        return (*env)->NewStringUTF(env, "{\"ok\":false,\"state\":\"error\",\"error\":\"null request\"}");
    }
    char *resp = MassStart((char *)raw);
    (*env)->ReleaseStringUTFChars(env, reqJson, raw);
    return to_jstring_and_free(env, resp);
}

JNIEXPORT jstring JNICALL
Java_com_afinn_mass_MassBridge_nativeStop(JNIEnv *env, jobject thiz) {
    (void)thiz;
    return to_jstring_and_free(env, MassStop());
}

JNIEXPORT jstring JNICALL
Java_com_afinn_mass_MassBridge_nativeStatus(JNIEnv *env, jobject thiz) {
    (void)thiz;
    return to_jstring_and_free(env, MassStatus());
}
