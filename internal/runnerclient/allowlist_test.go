package runnerclient

import (
	"reflect"
	"sort"
	"testing"
)

// TestClientExportsOnlyNamedRunnerOperations 防止重新引入任意 method/path/URL 代理入口。
func TestClientExportsOnlyNamedRunnerOperations(t *testing.T) {
	typeOfClient := reflect.TypeOf((*Client)(nil))
	methods := make([]string, 0, typeOfClient.NumMethod())
	for index := 0; index < typeOfClient.NumMethod(); index++ {
		methods = append(methods, typeOfClient.Method(index).Name)
	}
	sort.Strings(methods)
	want := []string{
		"Cancel", "Capabilities", "Delete", "DialPTY", "DirectoryList", "Download",
		"ExecuteBackground", "ExecuteForeground", "FileStat", "GoString",
		"Health", "Logs", "Mkdir", "Move", "Proxy", "Shutdown", "Status", "String",
		"Upload",
	}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("runner client exported surface changed: got %v want %v", methods, want)
	}
	structType := typeOfClient.Elem()
	for index := 0; index < structType.NumField(); index++ {
		if structType.Field(index).IsExported() {
			t.Fatalf("caller can mutate runner client field %s", structType.Field(index).Name)
		}
	}
}
