package journal

import "testing"

func TestDurableSingleAuthority(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put("prepare/claim", map[string]string{"state": "Preparing"}); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(dir); err == nil {
		other.Close()
		t.Fatal("second writer acquired authority")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var record map[string]string
	found, err := reopened.Get("prepare/claim", &record)
	if err != nil || !found || record["state"] != "Preparing" {
		t.Fatalf("lost committed intent: %v %v %v", found, record, err)
	}
}
