package mongodb

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func fixture(t *testing.T) *Session {
	t.Helper()
	if os.Getenv("NYADB_MONGODB_IT") != "1" {
		t.Skip("requires explicit disposable MongoDB fixture")
	}
	port, err := strconv.Atoi(os.Getenv("NYADB_MONGODB_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("disposable loopback port required")
	}
	p, _ := connections.Defaults(connections.MongoDB)
	p.ID = 73
	p.Name = "MongoDB fixture"
	p.Host = "127.0.0.1"
	p.Port = uint16(port)
	p.Username = os.Getenv("NYADB_MONGODB_USERNAME")
	p.MongoDBOptions.DirectConnection = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	v, err := OpenSession(ctx, p, os.Getenv("NYADB_MONGODB_PASSWORD"))
	if err != nil {
		t.Fatal(err)
	}
	s := v.(*Session)
	t.Cleanup(func() { s.Close() })
	return s
}
func TestMongoDBLiveDocumentLifecycle(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	db := "nyadb_it_" + bson.NewObjectID().Hex()
	collection := s.client.Database(db).Collection("records")
	t.Cleanup(func() { s.client.Database(db).Drop(context.Background()) })
	text := `{"_id":{"$oid":"507f1f77bcf86cd799439011"},"big":{"$numberLong":"9007199254740993"},"decimal":{"$numberDecimal":"123.1234567890123456789012345678"},"date":{"$date":{"$numberLong":"1700000000123"}},"binary":{"$binary":{"base64":"AP8=","subType":"80"}},"nil":null,"nested":{"array":[1,"x",true]}}`
	raw, err := parseDocument(text, contract.DataEditMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = collection.InsertOne(ctx, raw); err != nil {
		t.Fatal(safeError(err))
	}
	objects, err := s.Browse(ctx, s.Namespace(db))
	if err != nil || len(objects) != 1 || objects[0].Kind != "集合" || objects[0].Ref.Kind != "collection" {
		t.Fatal(objects, err)
	}
	o := objects[0].Ref
	meta, err := s.TableMetadata(ctx, o)
	if err != nil || !meta.Editable || len(meta.Indexes) != 1 || meta.Definition == "" {
		t.Fatal(meta, err)
	}
	p, err := s.page(ctx, s.ProfileID(), db, "records", contract.DataOptions{Object: o, Limit: 10})
	if err != nil || len(p.Rows) != 1 || p.RowSnapshots[0] == nil {
		t.Fatal(p, err)
	}
	full, err := s.row(ctx, s.ProfileID(), db, "records", *p.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	actual, err := parseDocument(full.Values[documentField].Text, contract.DataEditMaxBytes)
	if err != nil || !bytes.Equal(actual, raw) {
		t.Fatal("BSON types lost", err)
	}
	newText := strings.Replace(full.Values[documentField].Text, `"nil":null`, `"nil":"changed"`, 1)
	saved, err := s.save(ctx, s.ProfileID(), db, "records", []contract.DataEdit{{Kind: "update", Original: p.RowSnapshots[0], Values: map[string]contract.DataValue{documentField: {Kind: contract.JSON, Text: newText}}}})
	if err != nil || saved == nil || !saved.CommitConfirmed || saved.Applied != 1 {
		t.Fatal(saved, err)
	}
	if _, err = s.save(ctx, s.ProfileID(), db, "records", []contract.DataEdit{{Kind: "delete", Original: p.RowSnapshots[0]}}); err == nil {
		t.Fatal("stale snapshot deleted document")
	}
	verification, err := s.verify(ctx, s.ProfileID(), db, "records", saved)
	if err != nil || verification.Status != "applied" {
		t.Fatal(verification, err)
	}
	inserted, err := s.save(ctx, s.ProfileID(), db, "records", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{documentField: {Kind: contract.JSON, Text: `{"text":"new"}`}}}})
	if err != nil || inserted.Outcomes[0].Saved == nil {
		t.Fatal(inserted, err)
	}
	deleted, err := s.save(ctx, s.ProfileID(), db, "records", []contract.DataEdit{{Kind: "delete", Original: inserted.Outcomes[0].Saved}})
	if err != nil || !deleted.CommitConfirmed {
		t.Fatal(deleted, err)
	}
	var export bytes.Buffer
	if err = s.jsonl(ctx, o, &export); err != nil {
		t.Fatal(err)
	}
	exported, err := parseDocument(strings.TrimSpace(export.String()), contract.DataEditMaxBytes)
	if err != nil || exported.Lookup("big").Type != bson.TypeInt64 || exported.Lookup("missing").Type != 0 {
		t.Fatal("JSONL lost type/missing", err)
	}
	large := bson.D{{Key: "_id", Value: 2}, {Key: "text", Value: strings.Repeat("猫", 3000)}}
	if _, err = collection.InsertOne(ctx, large); err != nil {
		t.Fatal(err)
	}
	p, err = s.page(ctx, s.ProfileID(), db, "records", contract.DataOptions{Object: o, Limit: 1})
	if err != nil || !p.HasMore || !p.Rows[0][1].Truncated || len(p.RowSnapshots[0].Original) != 0 {
		t.Fatal("page not bounded", p, err)
	}
	chunk, err := s.chunk(ctx, s.ProfileID(), db, "records", documentField, *p.RowRefs[0], 0, 1024)
	if err != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore {
		t.Fatal(chunk, err)
	}
	lease, err := s.ReserveQuery(5)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	result, err := lease.Execute(ctx, db, `{"find":"records","filter":{},"batchSize":1}`)
	if err != nil || len(result.Sets) != 1 || len(result.Sets[0].Rows) != 2 {
		t.Fatal(result, err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err = lease.Execute(canceled, db, `{"ping":1}`); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if _, err = lease.Execute(ctx, db, `{"create":"noid","viewOn":"records","pipeline":[{"$project":{"_id":0,"text":1}}]}`); err != nil {
		t.Fatal(err)
	}
	view := s.Namespace(db).Object("view", "noid")
	vp, err := s.page(ctx, s.ProfileID(), db, "noid", contract.DataOptions{Object: view, Limit: 10})
	if err != nil || *vp.Editable || len(vp.Rows) != 2 || vp.RowSnapshots[0] != nil {
		t.Fatal("view without _id", vp, err)
	}
	if s.Capabilities().Transactions {
		t.Fatal("standalone transaction capability overstated")
	}
	batch := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{documentField: {Kind: contract.JSON, Text: `{"_id":100,"x":"prefix"}`}}}, {Kind: "insert", Values: map[string]contract.DataValue{documentField: {Kind: contract.JSON, Text: `{"_id":100,"x":"duplicate"}`}}}}
	partial, err := s.save(ctx, s.ProfileID(), db, "records", batch)
	if err == nil || partial.Applied != 1 || !partial.Incomplete || partial.CommitConfirmed || partial.CommitUncertain {
		t.Fatal("partial batch was misreported", partial, err)
	}
	v, err := s.verify(ctx, s.ProfileID(), db, "records", partial)
	if err != nil || v.Status != "indeterminate" {
		t.Fatal("partial batch allowed automatic replay", v, err)
	}
}
