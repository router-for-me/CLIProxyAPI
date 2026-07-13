package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type requestTableColumn struct {
	header string
	width  int
	min    int
	right  bool
}

var wideRequestTableColumns = []requestTableColumn{
	{header: "PROVIDER", width: 10, min: 8},
	{header: "MODEL", width: 20, min: 12},
	{header: "EFFORT", width: 8, min: 6},
	{header: "INPUT", width: 11, min: 5, right: true},
	{header: "OUTPUT", width: 11, min: 6, right: true},
	{header: "CACHE_READ", width: 12, min: 10, right: true},
	{header: "CACHE_WRITE", width: 12, min: 11, right: true},
	{header: "CACHE READ %", width: 12, min: 12, right: true},
	{header: "COST IN", width: 11, min: 8, right: true},
	{header: "COST OUT", width: 11, min: 8, right: true},
	{header: "COST CACHE", width: 12, min: 10, right: true},
}

var narrowRequestTableColumns = []requestTableColumn{
	{header: "PROV", width: 6, min: 3},
	{header: "MODEL", width: 10, min: 8},
	{header: "EFFORT", width: 6, min: 3},
	{header: "IN", width: 7, min: 5, right: true},
	{header: "OUT", width: 7, min: 5, right: true},
	{header: "CACHE_R", width: 8, min: 6, right: true},
	{header: "CACHE_W", width: 8, min: 6, right: true},
	{header: "CACHE %", width: 8, min: 6, right: true},
	{header: "$ IN", width: 8, min: 8, right: true},
	{header: "$ OUT", width: 8, min: 8, right: true},
	{header: "$ CACHE", width: 8, min: 8, right: true},
}

func requestTableColumns(availableWidth int) []requestTableColumn {
	availableWidth = max(0, availableWidth)
	template := wideRequestTableColumns
	if availableWidth < requestTableMinimumWidth(template) {
		template = narrowRequestTableColumns
	}
	columns := append([]requestTableColumn(nil), template...)
	overflow := requestTableWidth(columns) - availableWidth
	if overflow <= 0 {
		return columns
	}

	// Preserve numeric labels for as long as possible. Model, provider, and
	// effort values are safe to truncate first because the full values remain
	// available in request logs and the management API.
	for _, index := range []int{1, 0, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		if overflow <= 0 {
			break
		}
		shrink := columns[index].width - columns[index].min
		if shrink > overflow {
			shrink = overflow
		}
		columns[index].width -= shrink
		overflow -= shrink
	}
	if overflow <= 0 {
		return columns
	}

	// Below the compact layout's minimum width, remove secondary metrics instead
	// of allowing the table to spill outside the viewport. Keep the identifying
	// model column until last so even extremely narrow terminals remain useful.
	for _, index := range []int{10, 9, 8, 6, 7, 5, 2, 4, 3, 0} {
		columns[index].width = 0
		if requestTableWidth(columns) <= availableWidth {
			return columns
		}
	}

	// A viewport can be narrower than the final model column. Preserve as much
	// of that value as possible, including the one-cell ellipsis case.
	if availableWidth > 0 {
		columns[1].width = min(columns[1].width, availableWidth)
	} else {
		columns[1].width = 0
	}
	return columns
}

func requestTableMinimumWidth(columns []requestTableColumn) int {
	width := 0
	visible := 0
	for _, column := range columns {
		if column.width <= 0 {
			continue
		}
		if visible > 0 {
			width++
		}
		width += column.min
		visible++
	}
	return width
}

func requestTableWidth(columns []requestTableColumn) int {
	width := 0
	visible := 0
	for _, column := range columns {
		if column.width <= 0 {
			continue
		}
		if visible > 0 {
			width++
		}
		width += column.width
		visible++
	}
	return width
}

func renderRequestTableHeader(availableWidth int) string {
	columns := requestTableColumns(availableWidth)
	values := make([]string, len(columns))
	for index, column := range columns {
		values[index] = column.header
	}
	return tableHeaderStyle.Render(formatRequestTableCells(columns, values))
}

func formatRequestTableRow(event observabilityEvent, availableWidth int) string {
	provider := strings.TrimSpace(event.Provider)
	switch event.Operation {
	case "compaction":
		provider = "↻ " + provider
	case "compaction_reset":
		provider = "↺ " + provider
	}
	if event.Failed {
		provider = "! " + provider
	}
	if provider == "" {
		provider = "—"
	}

	effort := strings.TrimSpace(event.Effort)
	if effort == "" {
		effort = "—"
	}
	cachePercent := "—"
	if event.CacheTelemetryPresent {
		cachePercent = fmt.Sprintf("%.1f%%", event.CacheReadPercent)
	}

	costIn, costOut, costCache := "—", "—", "—"
	componentCost := event.EstimatedInputCostUSD + event.EstimatedOutputCostUSD + event.EstimatedCacheCostUSD
	// Older servers exposed only the total estimate. When attached across
	// versions, leave the component cells unavailable instead of rendering
	// misleading zeroes.
	if event.EstimatedCostAvailable && (componentCost != 0 || event.EstimatedCostUSD == 0) {
		costIn = formatRequestCost(event.EstimatedInputCostUSD)
		costOut = formatRequestCost(event.EstimatedOutputCostUSD)
		costCache = formatRequestCost(event.EstimatedCacheCostUSD)
	}
	cacheWrite := formatRequestTokens(event.CacheWriteTokens)
	if event.CacheWriteEstimated {
		cacheWrite = "~" + cacheWrite
	}

	values := []string{
		provider,
		strings.TrimSpace(event.Model),
		effort,
		formatRequestTokens(event.InputTokens),
		formatRequestTokens(event.OutputTokens),
		formatRequestTokens(event.CacheReadTokens),
		cacheWrite,
		cachePercent,
		costIn,
		costOut,
		costCache,
	}
	return formatRequestTableCells(requestTableColumns(availableWidth), values)
}

func styleRequestTableRow(event observabilityEvent, line string) string {
	if event.Failed || event.CacheMiss || event.CacheLowReuse {
		return logErrorStyle.Render(line)
	}
	if event.Operation == "compaction" || event.Operation == "compaction_reset" {
		return logCompactionStyle.Render(line)
	}
	return tableCellStyle.PaddingRight(0).Render(line)
}

func formatRequestTableCells(columns []requestTableColumn, values []string) string {
	var row strings.Builder
	visible := 0
	for index, column := range columns {
		if column.width <= 0 {
			continue
		}
		if visible > 0 {
			row.WriteByte(' ')
		}
		value := ""
		if index < len(values) {
			value = values[index]
		}
		row.WriteString(formatRequestTableCell(value, column.width, column.right))
		visible++
	}
	return strings.TrimRight(row.String(), " ")
}

func formatRequestTableCell(value string, width int, right bool) string {
	if width <= 0 {
		return ""
	}
	value = truncateTableCell(value, width)
	padding := width - lipgloss.Width(value)
	if padding <= 0 {
		return value
	}
	if right {
		return strings.Repeat(" ", padding) + value
	}
	return value + strings.Repeat(" ", padding)
}

func truncateTableCell(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return fitStringWidth(value, width-1) + "…"
}

func formatRequestTokens(tokens int64) string {
	negative := tokens < 0
	if negative {
		tokens = -tokens
	}
	digits := strconv.FormatInt(tokens, 10)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	if negative {
		return "-" + digits
	}
	return digits
}

func formatRequestCost(cost float64) string {
	if cost > 0 && cost < 0.0001 {
		return strings.Replace(fmt.Sprintf("$%.2e", cost), "e-0", "e-", 1)
	}
	return fmt.Sprintf("$%.4f", cost)
}
