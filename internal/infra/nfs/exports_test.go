package nfs

import (
	"strings"
	"testing"
)

func TestExportsKeepVolumeIdentityAndHideBacking(t *testing.T) {
	got, err := renderExports(map[string]int{"pvc-b": 102, "pvc-a": 101}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Export_Id = 101", "Pseudo = /data/volumes/pvc-a", "Path = /mnt/cloud-volumes/pvc-b"} {
		if !strings.Contains(got, want) {
			t.Fatal(got)
		}
	}
	if strings.Contains(got, "Path = /export;") {
		t.Fatal("backing image files exposed")
	}
	if _, err := renderExports(map[string]int{"../bad": 101}, false); err == nil {
		t.Fatal("unsafe identifier accepted")
	}
}
