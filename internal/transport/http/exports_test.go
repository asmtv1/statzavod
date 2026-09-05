package httpserver

import (
	"bytes"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func TestCreatorWorkbookExcelizeReopenRUENAndEmpty(t *testing.T) {
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.August, 10, 0, 0, 0, 0, time.UTC)
	published := time.Date(2026, time.August, 5, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name, locale              string
		summarySheet, pubsSheet   string
		title, viewsHeader        string
		emptyMessage              string
		publications              []creatorExportPublication
		wantPublicationDate, rows string
	}{
		{
			name: "russian", locale: "ru", summarySheet: "Сводка", pubsSheet: "Публикации",
			title: "Отчёт по креаторам", viewsHeader: "Просмотры", emptyMessage: "",
			publications:        []creatorExportPublication{{Title: "Публикация", Creator: "Тестовый креатор", Platform: "VK", Published: published, Views: 321, Likes: 45}},
			wantPublicationDate: "2026-08-05", rows: "1",
		},
		{
			name: "english-empty", locale: "en", summarySheet: "Summary", pubsSheet: "Publications",
			title: "Creator report", viewsHeader: "Views", emptyMessage: "No publications in the selected period",
			publications: nil, wantPublicationDate: "", rows: "0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/export?locale="+test.locale, nil)
			workbook := excelize.NewFile()
			err := buildCreatorWorkbook(
				workbook, request, []string{"creator-1"}, map[string]string{"creator-1": "Тестовый креатор"},
				map[string][5]int64{"creator-1": {321, 45, 6, 7, int64(len(test.publications))}},
				test.publications, exportPeriod{From: &from, To: &to},
			)
			if err != nil {
				t.Fatal(err)
			}
			buffer, err := workbook.WriteToBuffer()
			if err != nil {
				t.Fatal(err)
			}
			if err = workbook.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := excelize.OpenReader(bytes.NewReader(buffer.Bytes()))
			if err != nil {
				t.Fatalf("Excelize could not reopen workbook: %v", err)
			}
			defer func() {
				if closeErr := reopened.Close(); closeErr != nil {
					t.Errorf("close reopened workbook: %v", closeErr)
				}
			}()
			sheets := reopened.GetSheetList()
			if len(sheets) != 2 || sheets[0] != test.summarySheet || sheets[1] != test.pubsSheet {
				t.Fatalf("sheets=%v", sheets)
			}
			for cell, want := range map[string]string{"A1": test.title, "B4": test.viewsHeader, "A5": "Тестовый креатор", "B2": "2026-08-01", "D2": "2026-08-10"} {
				got, getErr := reopened.GetCellValue(test.summarySheet, cell)
				if getErr != nil {
					t.Fatal(getErr)
				}
				if got != want {
					t.Fatalf("%s!%s=%q, want %q", test.summarySheet, cell, got, want)
				}
			}
			for cell, want := range map[string]string{"B5": "321", "C5": "45", "D5": "6", "E5": "7", "F5": test.rows} {
				got, getErr := reopened.GetCellValue(test.summarySheet, cell)
				if getErr != nil {
					t.Fatal(getErr)
				}
				if got != want {
					t.Fatalf("%s!%s=%q, want %q", test.summarySheet, cell, got, want)
				}
				cellType, typeErr := reopened.GetCellType(test.summarySheet, cell)
				if typeErr != nil {
					t.Fatal(typeErr)
				}
				if cellType != excelize.CellTypeNumber && cellType != excelize.CellTypeUnset {
					t.Fatalf("%s!%s type=%v, want number", test.summarySheet, cell, cellType)
				}
			}
			if test.emptyMessage != "" {
				got, getErr := reopened.GetCellValue(test.pubsSheet, "A2")
				if getErr != nil || got != test.emptyMessage {
					t.Fatalf("empty report message=%q error=%v", got, getErr)
				}
				rows, rowsErr := reopened.GetRows(test.pubsSheet)
				if rowsErr != nil {
					t.Fatal(rowsErr)
				}
				if len(rows) != 2 {
					t.Fatalf("empty report rows=%d, want header + message", len(rows))
				}
			} else {
				got, getErr := reopened.GetCellValue(test.pubsSheet, "D2")
				if getErr != nil || got != test.wantPublicationDate {
					t.Fatalf("publication date=%q error=%v", got, getErr)
				}
				for _, cell := range []string{"E2", "F2"} {
					cellType, typeErr := reopened.GetCellType(test.pubsSheet, cell)
					if typeErr != nil || (cellType != excelize.CellTypeNumber && cellType != excelize.CellTypeUnset) {
						t.Fatalf("publication metric %s type=%v error=%v", cell, cellType, typeErr)
					}
				}
			}
		})
	}
}

func TestParseExportPeriod(t *testing.T) {
	for _, test := range []struct {
		query string
		valid bool
	}{
		{"activityFrom=2026-08-01&activityTo=2026-08-11", true},
		{"activityFrom=2026-08-11&activityTo=2026-08-11", true},
		{"activityFrom=2026-08-12&activityTo=2026-08-11", false},
		{"activityFrom=11.08.2026", false},
	} {
		request := httptest.NewRequest("GET", "/?"+test.query, nil)
		period, err := parseExportPeriod(request)
		if (err == nil) != test.valid {
			t.Fatalf("query %q error=%v valid=%v", test.query, err, test.valid)
		}
		if test.query == "activityFrom=2026-08-11&activityTo=2026-08-11" && (period.From == nil || period.To == nil) {
			t.Fatalf("equal date range lost a boundary: %#v", period)
		}
	}
}

func TestWriteCreatorWorkbookVerificationFixture(t *testing.T) {
	path := os.Getenv("STATZAVOD_XLSX_VERIFY_PATH")
	if path == "" {
		t.Skip("STATZAVOD_XLSX_VERIFY_PATH is not set")
	}
	request := httptest.NewRequest("GET", "/?locale=en", nil)
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.August, 10, 0, 0, 0, 0, time.UTC)
	workbook := excelize.NewFile()
	if err := buildCreatorWorkbook(
		workbook, request, []string{"creator-verify"}, map[string]string{"creator-verify": "Verification Creator"},
		map[string][5]int64{"creator-verify": {1234, 55, 6, 7, 1}},
		[]creatorExportPublication{{Title: "Verification publication", Creator: "Verification Creator", Platform: "YOUTUBE", Published: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC), Views: 1234, Likes: 55}},
		exportPeriod{From: &from, To: &to},
	); err != nil {
		t.Fatal(err)
	}
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	if err = workbook.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
