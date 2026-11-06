package mongodb

import (
	"context"
	"errors"
	"fmt"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

type preparedEdit struct {
	edit   contract.DataEdit
	raw    bson.Raw
	object contract.ObjectRef
}

func (s *Session) prepareEdits(id int64, db, name string, edits []contract.DataEdit) ([]preparedEdit, error) {
	if id != s.ProfileID() || len(edits) == 0 || len(edits) > contract.DataMaxPageSize || isSystemCollection(name) {
		return nil, connections.ErrInvalid
	}
	o := s.Namespace(db).Object("collection", name)
	if err := s.object(o); err != nil {
		return nil, err
	}
	prepared := make([]preparedEdit, 0, len(edits))
	for _, edit := range edits {
		p := preparedEdit{edit: edit, object: o}
		if edit.Kind != "insert" && edit.Kind != "update" && edit.Kind != "delete" {
			return nil, contract.ErrUnsupported
		}
		if edit.Kind != "insert" {
			if err := s.validateSnapshot(o, edit.Original); err != nil {
				return nil, err
			}
		} else if edit.Original != nil {
			return nil, connections.ErrInvalid
		}
		if edit.Kind == "delete" {
			if len(edit.Values) != 0 {
				return nil, connections.ErrInvalid
			}
		} else {
			v, ok := edit.Values[documentField]
			if !ok || len(edit.Values) != 1 || v.Null || v.Default || v.Binary || v.Kind != contract.JSON && v.Kind != contract.Text && v.Kind != "" {
				return nil, errors.New("edit the complete MongoDB document as Extended JSON")
			}
			raw, err := parseDocument(v.Text, contract.DataEditMaxBytes)
			if err != nil {
				return nil, err
			}
			if edit.Kind == "insert" && raw.Lookup("_id").Type == 0 {
				var doc bson.D
				if bson.Unmarshal(raw, &doc) != nil {
					return nil, connections.ErrInvalid
				}
				doc = append(bson.D{{Key: "_id", Value: bson.NewObjectID()}}, doc...)
				raw, err = bson.Marshal(doc)
				if err != nil {
					return nil, err
				}
			}
			if edit.Kind == "update" && !sameID(raw, bson.Raw(edit.Original.Ref.Values[0])) {
				return nil, errors.New("MongoDB _id is immutable")
			}
			p.raw = raw
		}
		prepared = append(prepared, p)
	}
	return prepared, nil
}

// Each document has an atomic compare-and-replace/delete. A batch reports
// confirmed prefixes and retains drafts if the complete batch is not confirmed.
// No write or failed commit is retried automatically.
func (s *Session) save(ctx context.Context, id int64, db, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	prepared, err := s.prepareEdits(id, db, name, edits)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	collection := s.client.Database(db).Collection(name, options.Collection().SetWriteConcern(writeconcern.Majority()))
	result := &contract.DataEditResult{}
	for _, p := range prepared {
		outcome := contract.DataEditOutcome{Kind: p.edit.Kind, Original: p.edit.Original, PreviewColumns: []string{"_id", documentField}}
		if p.raw != nil {
			outcome.Saved, err = s.snapshot(p.object, p.raw)
			if err != nil {
				return result, err
			}
			outcome.Previews, err = previews(p.raw)
			if err != nil {
				return result, err
			}
		}
		var original bson.Raw
		if p.edit.Kind != "insert" {
			filter, e := s.refFilter(p.object, p.edit.Original.Ref)
			if e != nil {
				return result, e
			}
			err = collection.FindOne(ctx, filter).Decode(&original)
			if err != nil || !matchesSnapshot(original, p.edit.Original) {
				result.Incomplete = result.Applied > 0
				if err != nil {
					return result, safeError(err)
				}
				return result, fmt.Errorf("%w: MongoDB document changed", contract.ErrDataConflict)
			}
			text, e := extendedJSON(original)
			if e != nil {
				return result, e
			}
			if len(text) > contract.DataEditMaxBytes {
				return result, errors.New("complete MongoDB document exceeds 1 MiB editing limit")
			}
		}
		var writeErr error
		switch p.edit.Kind {
		case "insert":
			_, writeErr = collection.InsertOne(ctx, p.raw)
		case "update", "delete":
			filter := bson.D{{Key: "_id", Value: original.Lookup("_id")}, {Key: "$expr", Value: bson.D{{Key: "$eq", Value: bson.A{"$$ROOT", bson.D{{Key: "$literal", Value: original}}}}}}}
			matched := int64(0)
			if p.edit.Kind == "update" {
				var r *mongo.UpdateResult
				r, writeErr = collection.ReplaceOne(ctx, filter, p.raw)
				if r != nil {
					matched = r.MatchedCount
				}
			} else {
				var r *mongo.DeleteResult
				r, writeErr = collection.DeleteOne(ctx, filter)
				if r != nil {
					matched = r.DeletedCount
				}
			}
			if writeErr == nil && matched != 1 {
				result.Incomplete = result.Applied > 0
				return result, fmt.Errorf("%w: MongoDB document changed during save", contract.ErrDataConflict)
			}
		}
		if writeErr != nil {
			// A server write error without a concern error confirms failure.
			var confirmed mongo.WriteException
			result.CommitUncertain = !errors.As(writeErr, &confirmed) || confirmed.WriteConcernError != nil
			result.Incomplete = result.Applied > 0 || result.CommitUncertain && len(prepared) > result.Applied+1
			if result.CommitUncertain {
				result.Outcomes = append(result.Outcomes, outcome)
			}
			return result, safeError(writeErr)
		}
		result.Applied++
		result.Outcomes = append(result.Outcomes, outcome)
	}
	result.CommitConfirmed = true
	return result, nil
}
func (s *Session) verify(ctx context.Context, id int64, db, name string, result *contract.DataEditResult) (*contract.DataVerification, error) {
	if id != s.ProfileID() || result == nil || len(result.Outcomes) == 0 {
		return nil, connections.ErrInvalid
	}
	o := s.Namespace(db).Object("collection", name)
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	verification := &contract.DataVerification{Status: "indeterminate"}
	allApplied, allAbsent := true, true
	for _, out := range result.Outcomes {
		refSnap := out.Saved
		if refSnap == nil {
			refSnap = out.Original
		}
		if err = s.validateSnapshot(o, refSnap); err != nil {
			return nil, err
		}
		if out.Original != nil {
			if err = s.validateSnapshot(o, out.Original); err != nil {
				return nil, err
			}
		}
		filter, err := s.refFilter(o, refSnap.Ref)
		if err != nil {
			return nil, err
		}
		var raw bson.Raw
		err = s.client.Database(db).Collection(name).FindOne(ctx, filter).Decode(&raw)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, safeError(err)
		}
		state := "indeterminate"
		if out.Kind == "delete" && errors.Is(err, mongo.ErrNoDocuments) || out.Kind != "delete" && err == nil && matchesSnapshot(raw, out.Saved) {
			state = "applied"
		} else if out.Kind == "insert" && errors.Is(err, mongo.ErrNoDocuments) || out.Kind != "insert" && err == nil && matchesSnapshot(raw, out.Original) {
			state = "not_applied"
		}
		verification.States = append(verification.States, state)
		allApplied = allApplied && state == "applied"
		allAbsent = allAbsent && state == "not_applied"
	}
	if allApplied && !result.Incomplete {
		verification.Status = "applied"
		recovered := *result
		recovered.CommitConfirmed = true
		recovered.CommitUncertain = false
		recovered.Verified = true
		recovered.Applied = len(result.Outcomes)
		verification.Recovered = &recovered
	} else if allAbsent {
		verification.Status = "not_applied"
	}
	return verification, nil
}
