package agent

import (
	"strings"
	"testing"
)

func TestSyncServeArgs(t *testing.T) {
	got, err := SyncServeArgs("/srv/backup", "rsync --server -logDtpre.iLsfxCIvu --delete --partial . /")
	if err != nil || strings.Join(got, " ") != "rsync --server -logDtpre.iLsfxCIvu --delete --partial . /srv/backup/" {
		t.Fatalf("%v %v", got, err)
	}
	got, err = SyncServeArgs("/srv/backup", "rsync --server -logDtpre.iLsfxCIvu . site/www")
	if err != nil || got[len(got)-1] != "/srv/backup/site/www/" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{
		"sh -c id",
		"rsync --server --sender -logDtpre.iLsfxCIvu . /",
		"rsync --server -logDtpre.iLsfxCIvu . ../etc",
		"rsync --server --log-file=/etc/x -logDtpre . /",
		"rsync --server -logDtpre.iLsfxCIvu .",
		"rsync --server -logDtpre.iLsfxCIvu /",
	} {
		if _, err := SyncServeArgs("/srv/backup", bad); err == nil {
			t.Errorf("allowed: %s", bad)
		}
	}
	for _, d := range []string{"/", "/etc", "/etc/x", "/var/lib/berth", "/srv/a b", "relative", "/srv/../etc"} {
		if ValidSyncDir(d) {
			t.Errorf("dir allowed: %s", d)
		}
	}
	if !ValidSyncDir("/srv/backup/web1") {
		t.Error("normal dir refused")
	}
}
