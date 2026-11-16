package panel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func TestQuotaAndOwnership(t *testing.T) {
	c := newTC(t)
	a := c.app
	ops := c.mkUser("ops", "pw-ops-123456", RoleAdmin)
	boss := c.mkUser("boss", "pw-boss-123456", RoleSuper)
	asOps := change.Actor{Type: "user", ID: itoa(ops.ID), Name: "ops"}
	a.db.Exec(`UPDATE users SET quota=? WHERE id=?`, `{"vms":1,"cpus":2,"mem_mb":2048}`, ops.ID)

	vm := json.RawMessage(`{"name":"a","image":"debian","size":"small"}`) // 1 cpu, 512 MB
	if err := a.checkQuota(asOps, "local", "vm.create", vm); err != nil {
		t.Fatalf("first VM refused: %v", err)
	}
	// it was made: the owners table follows the finished change
	a.recordOwner(&change.Change{Kind: "vm.create", Host: "local", Status: "done", Actor: asOps, Params: vm})
	u := a.usageOf(ops.ID)
	if u.VMs != 1 || u.CPUs != 1 || u.MemMB != 512 {
		t.Fatalf("usage %+v", u)
	}
	err := a.checkQuota(asOps, "local", "vm.create", json.RawMessage(`{"name":"b","image":"debian","size":"small"}`))
	if e := errs.From(err); err == nil || e.Code != "quota.over_vm" {
		t.Fatalf("second VM: %v", err)
	}
	// growing the machine past the CPU quota is refused; within it is fine
	if err := a.checkQuota(asOps, "local", "vm.resize", json.RawMessage(`{"name":"a","cpus":3,"memory_mb":512}`)); errs.From(err).Code != "quota.over_cpus" {
		t.Fatalf("resize over cpu: %v", err)
	}
	if err := a.checkQuota(asOps, "local", "vm.resize", json.RawMessage(`{"name":"a","cpus":2,"memory_mb":1024}`)); err != nil {
		t.Fatalf("resize within: %v", err)
	}
	// super administrators have no quota
	if err := a.checkQuota(change.Actor{Type: "user", ID: itoa(boss.ID)}, "local", "vm.create", vm); err != nil {
		t.Fatalf("super refused: %v", err)
	}
	// deleting frees it
	a.recordOwner(&change.Change{Kind: "vm.delete", Host: "local", Status: "done", Actor: asOps, Params: json.RawMessage(`{"name":"a"}`)})
	if a.usageOf(ops.ID).VMs != 0 {
		t.Fatal("not freed")
	}
	// site scope: only listed sites and own ones
	ops.Scope.Sites = []int64{7}
	a.saveUser(ops)
	a.db.Exec(`INSERT INTO owners(host, type, id, user_id, created_at) VALUES('local','site','9',?,?)`, ops.ID, time.Now().Unix())
	for id, ok := range map[string]bool{"7": true, "9": true, "8": false} {
		err := a.checkQuota(asOps, "local", "site.delete", json.RawMessage(`{"id":`+id+`}`))
		if (err == nil) != ok || err != nil && !strings.Contains(errs.From(err).Code, "site_forbidden") {
			t.Fatalf("site %s: %v", id, err)
		}
	}
}
