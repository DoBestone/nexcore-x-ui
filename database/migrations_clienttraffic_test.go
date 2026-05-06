package database

// Migration regression tests — pin the v1.1.0 backfill behavior so a future
// schema edit can't silently drop existing customers' data. These run end-
// to-end through gormigrate against a fresh in-memory sqlite, so they catch
// both the AutoMigrate and the data-shuffling code in one shot.

import (
	"path/filepath"
	"testing"

	"nexcore-x-ui/database/model"
)

// fakeInbound builds a model.Inbound with the given clients[] embedded as
// raw JSON in Settings — same shape the real schema uses.
func fakeInbound(t *testing.T, port int, protocol model.Protocol, settings string, total, expiry int64, enable bool) *model.Inbound {
	return &model.Inbound{
		Port:       port,
		Protocol:   protocol,
		Tag:        "inbound-test-" + string(protocol),
		Enable:     enable,
		Total:      total,
		ExpiryTime: expiry,
		Settings:   settings,
	}
}

// TestMigration_Backfill_VLESSClients verifies that VLESS inbounds with
// settings.clients[] entries each get a row in client_traffics carrying
// the inbound's total/expiry as the per-client default, since old data
// has no per-client limits.
func TestMigration_Backfill_VLESSClients(t *testing.T) {
	dir := t.TempDir()
	if err := InitDB(filepath.Join(dir, "t.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	// Seed a VLESS inbound with two clients before triggering the
	// 0010 migration. (InitDB already ran migrations including 0010,
	// but client_traffics is empty because the inbound didn't exist
	// at migration time — so we drop client_traffics, write the inbound,
	// then call backfillClientTraffics directly.)
	db := GetDB()
	if err := db.Migrator().DropTable(&model.ClientTraffic{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ClientTraffic{}); err != nil {
		t.Fatal(err)
	}
	in := fakeInbound(t, 11001, model.VLESS,
		`{"clients":[{"id":"u1","email":"alice@x"},{"id":"u2","email":"bob@x"}]}`,
		10*1024*1024*1024, // 10 GB cap
		1700000000000,     // some expiry
		true,
	)
	if err := db.Create(in).Error; err != nil {
		t.Fatal(err)
	}

	if err := backfillClientTraffics(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var rows []model.ClientTraffic
	if err := db.Where("inbound_id = ?", in.Id).Order("email").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 client_traffics rows, got %d", len(rows))
	}
	if rows[0].Email != "alice@x" || rows[1].Email != "bob@x" {
		t.Fatalf("emails wrong: %+v", rows)
	}
	for _, r := range rows {
		if r.Up != 0 || r.Down != 0 {
			t.Errorf("backfilled row should start at 0/0, got up=%d down=%d", r.Up, r.Down)
		}
		if r.Total != in.Total {
			t.Errorf("total %d, want inbound total %d", r.Total, in.Total)
		}
		if r.ExpiryTime != in.ExpiryTime {
			t.Errorf("expiry mismatch")
		}
		if !r.Enable {
			t.Errorf("backfilled enable should default to inbound's enable=true")
		}
	}
}

// TestMigration_Backfill_Idempotent — running backfill twice produces no
// duplicates. Important because gormigrate could re-run the migration on
// some restore scenarios; we don't want each run to cause a UNIQUE
// constraint violation or duplicate rows.
func TestMigration_Backfill_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := InitDB(filepath.Join(dir, "t.db")); err != nil {
		t.Fatal(err)
	}
	db := GetDB()
	in := fakeInbound(t, 11002, model.Trojan,
		`{"clients":[{"password":"secret","email":"carol@x"}]}`,
		0, 0, true)
	if err := db.Create(in).Error; err != nil {
		t.Fatal(err)
	}

	if err := backfillClientTraffics(db); err != nil {
		t.Fatal(err)
	}
	if err := backfillClientTraffics(db); err != nil {
		t.Fatalf("second backfill should be no-op, got: %v", err)
	}
	var n int64
	if err := db.Model(&model.ClientTraffic{}).
		Where("email = ?", "carol@x").Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row, got %d (duplicate created on re-run)", n)
	}
}

// TestMigration_Backfill_SkipsEmaillessProtocols — Shadowsocks-legacy
// has just method+password (no clients[] / no email); we must skip those
// inbounds, not crash, not drop any rows.
func TestMigration_Backfill_SkipsEmaillessProtocols(t *testing.T) {
	dir := t.TempDir()
	if err := InitDB(filepath.Join(dir, "t.db")); err != nil {
		t.Fatal(err)
	}
	db := GetDB()
	in := fakeInbound(t, 11003, model.Shadowsocks,
		`{"method":"chacha20-ietf-poly1305","password":"abc"}`,
		0, 0, true)
	if err := db.Create(in).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillClientTraffics(db); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&model.ClientTraffic{}).
		Where("inbound_id = ?", in.Id).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("legacy SS inbound should produce 0 client_traffics rows, got %d", n)
	}
}

// TestMigration_Backfill_SkipsClientsWithoutEmail — clients[] entry with
// id but no email should be skipped (xray won't be able to attribute
// stats to it anyway). Don't crash, don't insert NULL email rows.
func TestMigration_Backfill_SkipsClientsWithoutEmail(t *testing.T) {
	dir := t.TempDir()
	if err := InitDB(filepath.Join(dir, "t.db")); err != nil {
		t.Fatal(err)
	}
	db := GetDB()
	in := fakeInbound(t, 11004, model.VLESS,
		`{"clients":[{"id":"u-no-email"},{"id":"u-with-email","email":"dave@x"}]}`,
		0, 0, true)
	if err := db.Create(in).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillClientTraffics(db); err != nil {
		t.Fatal(err)
	}
	var emails []string
	if err := db.Model(&model.ClientTraffic{}).
		Where("inbound_id = ?", in.Id).Pluck("email", &emails).Error; err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 || emails[0] != "dave@x" {
		t.Errorf("expected only dave@x, got %v", emails)
	}
}
