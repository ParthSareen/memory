package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type ExportRelationship struct {
	FromRecordID string `json:"from_record_id"`
	Type         string `json:"type"`
	ToRecordID   string `json:"to_record_id"`
	CreatedAt    string `json:"created_at"`
}

type ExportDocument struct {
	Format        string               `json:"format"`
	Version       int                  `json:"version"`
	ExportedAt    string               `json:"exported_at"`
	Records       []Record             `json:"records"`
	Relationships []ExportRelationship `json:"relationships"`
}

type ImportResult struct {
	Imported      int `json:"imported"`
	Skipped       int `json:"skipped"`
	Relationships int `json:"relationships"`
}

func (s *Store) ExportData() (ExportDocument, error) {
	rows, err := s.db.Query("SELECT " + recordColumns + " FROM records ORDER BY created_at, id")
	if err != nil {
		return ExportDocument{}, err
	}
	var records []Record
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			rows.Close()
			return ExportDocument{}, err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return ExportDocument{}, err
	}
	for index := range records {
		if err := s.hydrateCollections(&records[index]); err != nil {
			return ExportDocument{}, err
		}
		records[index].Relationships = nil
	}
	if records == nil {
		records = []Record{}
	}
	relationRows, err := s.db.Query(`
SELECT from_record_id, relationship_type, to_record_id, created_at
FROM relationships ORDER BY created_at, from_record_id, relationship_type, to_record_id`)
	if err != nil {
		return ExportDocument{}, err
	}
	defer relationRows.Close()
	relationships := []ExportRelationship{}
	for relationRows.Next() {
		var relationship ExportRelationship
		if err := relationRows.Scan(&relationship.FromRecordID, &relationship.Type, &relationship.ToRecordID, &relationship.CreatedAt); err != nil {
			return ExportDocument{}, err
		}
		relationships = append(relationships, relationship)
	}
	return ExportDocument{
		Format: ExportFormat, Version: ExportVersion, ExportedAt: utcNow(),
		Records: records, Relationships: relationships,
	}, relationRows.Err()
}

func (s *Store) ImportData(document ExportDocument, onConflict string) (ImportResult, error) {
	if document.Format != ExportFormat || document.Version != ExportVersion {
		return ImportResult{}, fmt.Errorf("unsupported import format; expected %s version %d", ExportFormat, ExportVersion)
	}
	if onConflict == "" {
		onConflict = "error"
	}
	if onConflict != "error" && onConflict != "skip" && onConflict != "replace" {
		return ImportResult{}, errors.New("on-conflict must be error, skip, or replace")
	}
	records := make([]Record, len(document.Records))
	for index, record := range document.Records {
		normalized, err := normalizeImportedRecord(record)
		if err != nil {
			return ImportResult{}, fmt.Errorf("record %d: %w", index, err)
		}
		records[index] = normalized
	}
	for index, relationship := range document.Relationships {
		if strings.TrimSpace(relationship.FromRecordID) == "" || strings.TrimSpace(relationship.ToRecordID) == "" {
			return ImportResult{}, fmt.Errorf("relationship %d: record ids are required", index)
		}
		if relationship.FromRecordID == relationship.ToRecordID {
			return ImportResult{}, fmt.Errorf("relationship %d: a record cannot relate to itself", index)
		}
		if _, err := NormalizeType(relationship.Type, "relationship type"); err != nil {
			return ImportResult{}, fmt.Errorf("relationship %d: %w", index, err)
		}
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{}
	fail := func(err error) (ImportResult, error) {
		tx.Rollback()
		return ImportResult{}, err
	}
	for _, record := range records {
		var exists int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM records WHERE id = ?", record.ID).Scan(&exists)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fail(err)
		}
		if err == nil {
			switch onConflict {
			case "error":
				return fail(fmt.Errorf("%w: record already exists: %s", ErrConflict, record.ID))
			case "skip":
				result.Skipped++
				continue
			case "replace":
				if err := replaceRecord(ctx, tx, record); err != nil {
					return fail(err)
				}
			}
		} else if err := insertRecord(ctx, tx, record); err != nil {
			return fail(err)
		}
		result.Imported++
	}
	for _, relationship := range document.Relationships {
		relationType, _ := NormalizeType(relationship.Type, "relationship type")
		createdAt := strings.TrimSpace(relationship.CreatedAt)
		if createdAt == "" {
			createdAt = utcNow()
		}
		statement := "INSERT INTO relationships(from_record_id, relationship_type, to_record_id, created_at) VALUES (?, ?, ?, ?)"
		if onConflict != "error" {
			statement = "INSERT OR IGNORE INTO relationships(from_record_id, relationship_type, to_record_id, created_at) VALUES (?, ?, ?, ?)"
		}
		sqlResult, err := tx.ExecContext(ctx, statement, relationship.FromRecordID, relationType, relationship.ToRecordID, createdAt)
		if err != nil {
			return fail(fmt.Errorf("import relationship: %w", err))
		}
		count, _ := sqlResult.RowsAffected()
		result.Relationships += int(count)
	}
	if err := tx.Commit(); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}
