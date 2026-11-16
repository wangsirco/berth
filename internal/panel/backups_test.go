package panel

import (
	"strings"
	"testing"
)

func TestTargetRepos(t *testing.T) {
	r, err := targetCfg{Kind: "local", Path: "/mnt/usb/berth", Password: "p"}.repo(1)
	if err != nil || r.URL != "/mnt/usb/berth" {
		t.Fatal(err, r)
	}
	r, err = targetCfg{Kind: "s3", Endpoint: "https://acc.r2.cloudflarestorage.com", Bucket: "bk", Path: "mini", Access: "a", Secret: "s", Password: "p"}.repo(1)
	if err != nil || r.URL != "s3:https://acc.r2.cloudflarestorage.com/bk/mini" || r.Env["AWS_SECRET_ACCESS_KEY"] != "s" {
		t.Fatal(err, r)
	}
	r, err = targetCfg{Kind: "rclone", Remote: "drive", Options: map[string]string{"scope": "drive"}, Token: `{"access_token":"x","expiry":"2026"}`, Path: "Berth/mini", Password: "p"}.repo(7)
	if err != nil || r.URL != "rclone:berth7:Berth/mini" || !strings.Contains(r.Rclone, "[berth7]\ntype = drive\n") || !strings.Contains(r.Rclone, `token = {"access_token"`) {
		t.Fatal(err, r.URL, r.Rclone)
	}
	for _, bad := range []targetCfg{
		{Kind: "local", Path: "relative"},
		{Kind: "s3", Endpoint: "https://x"},
		{Kind: "rclone", Remote: "drive", Token: "not json"},
		{Kind: "rclone", Remote: "drive\ntype = local"},
		{Kind: "rclone", Remote: "drive", Options: map[string]string{"scope": "drive\n[evil]"}},
		{Kind: "ftp"},
	} {
		bad.Password = "p"
		if _, err := bad.repo(1); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
