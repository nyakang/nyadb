package localdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestFavoriteIsPersistentAndDoesNotChangeConnectionSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "favourites.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := connections.NewService(NewProfileStore(db))
	ctx := context.Background()
	p, err := svc.Create(ctx, connections.Profile{Name: "Local", Kind: connections.MySQL, Host: "localhost", Port: 3306, Username: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Starred {
		t.Fatal("connections must start unstarred")
	}
	if err = svc.SetStarred(ctx, p.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc = connections.NewService(NewProfileStore(db))
	read, err := svc.Get(ctx, p.ID)
	if err != nil || !read.Starred || read.Host != p.Host || read.Username != p.Username {
		t.Fatalf("reloaded favorite/settings: %+v, %v", read, err)
	}
	if err = svc.SetStarred(ctx, p.ID, false); err != nil {
		t.Fatal(err)
	}
	read, err = svc.Get(ctx, p.ID)
	if err != nil || read.Starred {
		t.Fatalf("toggle favorite: %+v %v", read, err)
	}
	if err = svc.SetStarred(ctx, 0, true); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if err = svc.SetStarred(ctx, p.ID+100, true); err == nil {
		t.Fatal("missing profile accepted")
	}
}
