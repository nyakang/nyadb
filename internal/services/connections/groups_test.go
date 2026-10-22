package connections

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/storage/localdb"
)

func TestVirtualGroupsPersistRenameAndDeleteWithoutDroppingProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(NewStore(db))
	ctx := context.Background()
	created, err := s.Create(ctx, Profile{Name: "Local", Kind: MySQL, Host: "localhost", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup(ctx, "开发库"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup(ctx, "空组"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup(ctx, "开发库"); err == nil {
		t.Fatal("duplicate group accepted")
	}
	if err := s.CreateGroup(ctx, "../invalid"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid group: %v", err)
	}
	if err := s.MoveToGroup(ctx, created.ID, "不存在"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	if err := s.MoveToGroup(ctx, created.ID, "开发库"); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get(ctx, created.ID)
	if err != nil || p.GroupName != "开发库" {
		t.Fatalf("assignment: %+v %v", p, err)
	}
	duplicate, err := s.Duplicate(ctx, p.ID, "Local replica")
	if err != nil || duplicate.GroupName != "开发库" {
		t.Fatalf("duplicate should preserve group: %+v %v", duplicate, err)
	}
	if err := s.RenameGroup(ctx, "开发库", "测试库"); err != nil {
		t.Fatal(err)
	}
	p, err = s.Get(ctx, p.ID)
	if err != nil || p.GroupName != "测试库" {
		t.Fatalf("rename didn't preserve assignment: %+v %v", p, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewService(NewStore(db))
	groups, err := s.ListGroups(ctx)
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups not persisted: %+v %v", groups, err)
	}
	if err := s.DeleteGroup(ctx, "测试库"); err != nil {
		t.Fatal(err)
	}
	p, err = s.Get(ctx, p.ID)
	if err != nil || p.GroupName != "" {
		t.Fatalf("deleting folder must keep ungrouped profile: %+v %v", p, err)
	}
	duplicate, err = s.Get(ctx, duplicate.ID)
	if err != nil || duplicate.GroupName != "" {
		t.Fatalf("duplicate removed or still grouped: %+v %v", duplicate, err)
	}
	groups, err = s.ListGroups(ctx)
	if err != nil || len(groups) != 1 || groups[0].Name != "空组" {
		t.Fatalf("empty folder not preserved: %+v %v", groups, err)
	}
}
