package main

import "syscall"

// GetUserDefaultLangID reports the current user's regional format locale:
// https://learn.microsoft.com/en-us/windows/win32/api/winnls/nf-winnls-getuserdefaultlangid
var getUserDefaultLangID = syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultLangID")

func nativeRussianLocale() bool {
	langID, _, _ := getUserDefaultLangID.Call()
	// PRIMARYLANGID is the low ten bits; LANG_RUSSIAN is 0x19.
	return langID&0x3ff == 0x19
}
