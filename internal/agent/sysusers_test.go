package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoginUsers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "passwd")
	os.WriteFile(p, []byte("root:x:0:0:root:/root:/bin/bash\nberth:x:990:990::/var/lib/berth:/sbin/nologin\niwsheng:x:1000:1000::/home/iwsheng:/bin/bash\nnobody:x:65534:65534::/:/sbin/nologin\n"), 0o644)
	u := loginUsers(p)
	if len(u) != 1 || u[0].Name != "iwsheng" {
		t.Fatalf("%+v", u)
	}
}
