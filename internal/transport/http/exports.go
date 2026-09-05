package httpserver

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

type exportPeriod struct {
	From *time.Time
	To   *time.Time
}

type creatorExportPublication struct {
	Title, Creator, Platform string
	Published                time.Time
	Views, Likes             int64
}

func parseExportPeriod(r *http.Request) (exportPeriod, error) {
	var period exportPeriod
	values := []struct {
		value  string
		target **time.Time
	}{
		{value: strings.TrimSpace(r.URL.Query().Get("activityFrom")), target: &period.From},
		{value: strings.TrimSpace(r.URL.Query().Get("activityTo")), target: &period.To},
	}
	for _, item := range values {
		value, target := item.value, item.target
		if value == "" {
			continue
		}
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			return exportPeriod{}, fmt.Errorf("date %q must use YYYY-MM-DD: %w", value, err)
		}
		*target = &parsed
	}
	if period.From != nil && period.To != nil && period.From.After(*period.To) {
		return exportPeriod{}, fmt.Errorf("activityFrom must not be after activityTo")
	}
	return period, nil
}

func addPeriodSQL(query, alias string, args []any, period exportPeriod) (string, []any) {
	if period.From != nil {
		query += fmt.Sprintf(" AND %s.published_at >= $%d", alias, len(args)+1)
		args = append(args, *period.From)
	}
	if period.To != nil {
		query += fmt.Sprintf(" AND %s.published_at < ($%d::date + interval '1 day')", alias, len(args)+1)
		args = append(args, *period.To)
	}
	return query, args
}

func periodResponse(period exportPeriod) map[string]any {
	response := map[string]any{"from": nil, "to": nil}
	if period.From != nil {
		response["from"] = period.From.Format("2006-01-02")
	}
	if period.To != nil {
		response["to"] = period.To.Format("2006-01-02")
	}
	return response
}

func (s *Server) writeCreatorExport(w http.ResponseWriter, r *http.Request, p principal, creatorIDs []string, filename string) {
	period, err := parseExportPeriod(r)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid period", err.Error())
		return
	}

	creatorRows, err := s.pool.Query(r.Context(), `
		SELECT creator.id::text,creator.display_name
		FROM creators creator
		JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id
		WHERE creator.id::text=ANY($1) AND creator.organization_id=$2
		  AND creator.archived_at IS NULL AND company.archived_at IS NULL
		ORDER BY creator.display_name,creator.id`, creatorIDs, p.OrganizationID)
	if err != nil {
		problem(w, http.StatusInternalServerError, "export failed", "could not load creators")
		return
	}
	names := make(map[string]string, len(creatorIDs))
	orderedIDs := make([]string, 0, len(creatorIDs))
	for creatorRows.Next() {
		var id, name string
		if err = creatorRows.Scan(&id, &name); err != nil {
			creatorRows.Close()
			problem(w, http.StatusInternalServerError, "export failed", "could not read creators")
			return
		}
		names[id] = name
		orderedIDs = append(orderedIDs, id)
	}
	if err = creatorRows.Err(); err != nil {
		creatorRows.Close()
		problem(w, http.StatusInternalServerError, "export failed", "could not finish reading creators")
		return
	}
	creatorRows.Close()
	if len(names) != len(creatorIDs) {
		problem(w, http.StatusNotFound, "not found", "one or more creators do not exist in the active context")
		return
	}

	where := ` WHERE publication.creator_id::text=ANY($1) AND publication.organization_id=$2`
	args := []any{creatorIDs, p.OrganizationID}
	where, args = addPeriodSQL(where, "publication", args, period)
	summaryRows, err := s.pool.Query(r.Context(), `
		SELECT publication.creator_id::text,
		       COALESCE(sum(metric.views),0),COALESCE(sum(metric.likes),0),
		       COALESCE(sum(metric.comments),0),COALESCE(sum(metric.shares),0),
		       count(DISTINCT publication.id)
		FROM publications publication
		JOIN creators creator ON creator.id=publication.creator_id AND creator.organization_id=publication.organization_id
		JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id
		LEFT JOIN LATERAL (
			SELECT views,likes,comments,shares FROM publication_metric_snapshots
			WHERE publication_id=publication.id ORDER BY observed_at DESC LIMIT 1
		) metric ON true`+where+`
		  AND company.archived_at IS NULL
		GROUP BY publication.creator_id`, args...)
	if err != nil {
		problem(w, http.StatusInternalServerError, "export failed", "could not load KPI")
		return
	}
	summary := make(map[string][5]int64, len(creatorIDs))
	for summaryRows.Next() {
		var id string
		var values [5]int64
		if err = summaryRows.Scan(&id, &values[0], &values[1], &values[2], &values[3], &values[4]); err != nil {
			summaryRows.Close()
			problem(w, http.StatusInternalServerError, "export failed", "could not read KPI")
			return
		}
		summary[id] = values
	}
	if err = summaryRows.Err(); err != nil {
		summaryRows.Close()
		problem(w, http.StatusInternalServerError, "export failed", "could not finish reading KPI")
		return
	}
	summaryRows.Close()

	publicationRows, err := s.pool.Query(r.Context(), `
		SELECT COALESCE(publication.title,''),creator.display_name,publication.platform,
		       publication.published_at,COALESCE(metric.views,0),COALESCE(metric.likes,0)
		FROM publications publication
		JOIN creators creator ON creator.id=publication.creator_id AND creator.organization_id=publication.organization_id
		JOIN companies company ON company.id=creator.company_id AND company.organization_id=creator.organization_id
		LEFT JOIN LATERAL (
			SELECT views,likes FROM publication_metric_snapshots
			WHERE publication_id=publication.id ORDER BY observed_at DESC LIMIT 1
		) metric ON true`+where+`
		  AND company.archived_at IS NULL
		ORDER BY publication.published_at DESC,publication.id`, args...)
	if err != nil {
		problem(w, http.StatusInternalServerError, "export failed", "could not load publications")
		return
	}
	publications := make([]creatorExportPublication, 0)
	for publicationRows.Next() {
		var publication creatorExportPublication
		if err = publicationRows.Scan(&publication.Title, &publication.Creator, &publication.Platform, &publication.Published, &publication.Views, &publication.Likes); err != nil {
			publicationRows.Close()
			problem(w, http.StatusInternalServerError, "export failed", "could not read publications")
			return
		}
		publications = append(publications, publication)
	}
	if err = publicationRows.Err(); err != nil {
		publicationRows.Close()
		problem(w, http.StatusInternalServerError, "export failed", "could not finish reading publications")
		return
	}
	publicationRows.Close()

	workbook := excelize.NewFile()
	buildErr := buildCreatorWorkbook(workbook, r, orderedIDs, names, summary, publications, period)
	if buildErr != nil {
		if closeErr := workbook.Close(); closeErr != nil {
			problem(w, http.StatusInternalServerError, "export failed", "could not close failed Excel workbook")
			return
		}
		problem(w, http.StatusInternalServerError, "export failed", "could not build Excel workbook")
		return
	}
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		if closeErr := workbook.Close(); closeErr != nil {
			problem(w, http.StatusInternalServerError, "export failed", "could not close failed Excel workbook")
			return
		}
		problem(w, http.StatusInternalServerError, "export failed", "could not serialize Excel workbook")
		return
	}
	if err = workbook.Close(); err != nil {
		problem(w, http.StatusInternalServerError, "export failed", "could not finalize Excel workbook")
		return
	}

	// Headers are deliberately set only after every SQL read, workbook API call,
	// and XLSX serialization has succeeded.
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.Header().Set("Content-Length", fmt.Sprint(buffer.Len()))
	w.WriteHeader(http.StatusOK)
	if _, err = w.Write(buffer.Bytes()); err != nil {
		return
	}
}

func buildCreatorWorkbook(workbook *excelize.File, r *http.Request, orderedIDs []string, names map[string]string, summary map[string][5]int64, publications []creatorExportPublication, period exportPeriod) error {
	summarySheet := localized(r, "Сводка", "Summary")
	if err := workbook.SetSheetName("Sheet1", summarySheet); err != nil {
		return err
	}
	publicationSheet := localized(r, "Публикации", "Publications")
	publicationSheetIndex, err := workbook.NewSheet(publicationSheet)
	if err != nil {
		return err
	}
	if err = workbook.MergeCell(summarySheet, "A1", "F1"); err != nil {
		return err
	}
	if err = workbook.SetCellValue(summarySheet, "A1", localized(r, "Отчёт по креаторам", "Creator report")); err != nil {
		return err
	}
	if err = workbook.SetCellValue(summarySheet, "A2", localized(r, "Начало периода", "Period start")); err != nil {
		return err
	}
	if period.From != nil {
		if err = workbook.SetCellValue(summarySheet, "B2", *period.From); err != nil {
			return err
		}
	} else if err = workbook.SetCellValue(summarySheet, "B2", localized(r, "Все даты", "All dates")); err != nil {
		return err
	}
	if err = workbook.SetCellValue(summarySheet, "C2", localized(r, "Конец периода", "Period end")); err != nil {
		return err
	}
	if period.To != nil {
		if err = workbook.SetCellValue(summarySheet, "D2", *period.To); err != nil {
			return err
		}
	} else if err = workbook.SetCellValue(summarySheet, "D2", localized(r, "Все даты", "All dates")); err != nil {
		return err
	}
	headers := []string{
		localized(r, "Креатор", "Creator"), localized(r, "Просмотры", "Views"),
		localized(r, "Реакции", "Reactions"), localized(r, "Комментарии", "Comments"),
		localized(r, "Репосты", "Shares"), localized(r, "Публикации", "Publications"),
	}
	for column, value := range headers {
		cell, err := excelize.CoordinatesToCellName(column+1, 4)
		if err != nil {
			return err
		}
		if err = workbook.SetCellValue(summarySheet, cell, value); err != nil {
			return err
		}
	}
	for rowOffset, id := range orderedIDs {
		row := rowOffset + 5
		if err = workbook.SetCellValue(summarySheet, fmt.Sprintf("A%d", row), names[id]); err != nil {
			return err
		}
		for index, value := range summary[id] {
			cell, coordinateErr := excelize.CoordinatesToCellName(index+2, row)
			if coordinateErr != nil {
				return coordinateErr
			}
			if err = workbook.SetCellValue(summarySheet, cell, value); err != nil {
				return err
			}
		}
	}
	publicationHeaders := []string{
		localized(r, "Название", "Title"), localized(r, "Креатор", "Creator"),
		localized(r, "Платформа", "Platform"), localized(r, "Дата публикации", "Publication date"),
		localized(r, "Просмотры", "Views"), localized(r, "Реакции", "Reactions"),
	}
	for column, value := range publicationHeaders {
		cell, coordinateErr := excelize.CoordinatesToCellName(column+1, 1)
		if coordinateErr != nil {
			return coordinateErr
		}
		if err = workbook.SetCellValue(publicationSheet, cell, value); err != nil {
			return err
		}
	}
	if len(publications) == 0 {
		if err = workbook.SetCellValue(publicationSheet, "A2", localized(r, "Нет публикаций за выбранный период", "No publications in the selected period")); err != nil {
			return err
		}
	} else {
		for index, publication := range publications {
			row := index + 2
			values := []any{publication.Title, publication.Creator, publication.Platform, publication.Published, publication.Views, publication.Likes}
			for column, value := range values {
				cell, coordinateErr := excelize.CoordinatesToCellName(column+1, row)
				if coordinateErr != nil {
					return coordinateErr
				}
				if err = workbook.SetCellValue(publicationSheet, cell, value); err != nil {
					return err
				}
			}
		}
	}

	titleStyle, err := workbook.NewStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Color: []string{"#111213"}, Pattern: 1}, Font: &excelize.Font{Bold: true, Color: "#D6A84B", Size: 16}, Alignment: &excelize.Alignment{Vertical: "center"}})
	if err != nil {
		return err
	}
	headerStyle, err := workbook.NewStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Color: []string{"#2A2B2D"}, Pattern: 1}, Font: &excelize.Font{Bold: true, Color: "#FFFFFF"}, Alignment: &excelize.Alignment{Vertical: "center"}, Border: []excelize.Border{{Type: "bottom", Color: "#D6A84B", Style: 1}}})
	if err != nil {
		return err
	}
	numberStyle, err := workbook.NewStyle(&excelize.Style{NumFmt: 3})
	if err != nil {
		return err
	}
	dateStyle, err := workbook.NewStyle(&excelize.Style{CustomNumFmt: stringPointer("yyyy-mm-dd")})
	if err != nil {
		return err
	}
	if err = workbook.SetCellStyle(summarySheet, "A1", "F1", titleStyle); err != nil {
		return err
	}
	if err = workbook.SetCellStyle(summarySheet, "A4", "F4", headerStyle); err != nil {
		return err
	}
	if err = workbook.SetCellStyle(publicationSheet, "A1", "F1", headerStyle); err != nil {
		return err
	}
	if period.From != nil {
		if err = workbook.SetCellStyle(summarySheet, "B2", "B2", dateStyle); err != nil {
			return err
		}
	}
	if period.To != nil {
		if err = workbook.SetCellStyle(summarySheet, "D2", "D2", dateStyle); err != nil {
			return err
		}
	}
	if len(orderedIDs) > 0 {
		if err = workbook.SetCellStyle(summarySheet, "B5", fmt.Sprintf("F%d", len(orderedIDs)+4), numberStyle); err != nil {
			return err
		}
	}
	if len(publications) > 0 {
		if err = workbook.SetCellStyle(publicationSheet, "D2", fmt.Sprintf("D%d", len(publications)+1), dateStyle); err != nil {
			return err
		}
		if err = workbook.SetCellStyle(publicationSheet, "E2", fmt.Sprintf("F%d", len(publications)+1), numberStyle); err != nil {
			return err
		}
	}
	for _, sheet := range []string{summarySheet, publicationSheet} {
		if err = workbook.SetColWidth(sheet, "A", "A", 32); err != nil {
			return err
		}
		if err = workbook.SetColWidth(sheet, "B", "F", 20); err != nil {
			return err
		}
	}
	if err = workbook.SetRowHeight(summarySheet, 1, 28); err != nil {
		return err
	}
	if err = workbook.SetPanes(summarySheet, &excelize.Panes{Freeze: true, YSplit: 4, TopLeftCell: "A5", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	if err = workbook.SetPanes(publicationSheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	workbook.SetActiveSheet(publicationSheetIndex)
	return nil
}

func stringPointer(value string) *string { return &value }
