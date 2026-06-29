package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type analytics struct {
	mu   sync.Mutex
	now  func() time.Time
	path string
}

type analyticsLogEvent struct {
	Date  string `json:"d,omitempty"`
	Time  string `json:"t,omitempty"`
	Event string `json:"e,omitempty"`
	Old   string `json:"event,omitempty"`
}

type analyticsSummary struct {
	GeneratedAt       string
	ClipboardShares   int
	DevicesJoined     int
	Daily             []analyticsDay
	ChartDays         []analyticsDay
	Range             string
	RangeLabel        string
	RangeOptions      []analyticsRangeOption
	Chart             analyticsChart
	AnalyticsDisabled bool
	Nonce             string
}

type analyticsDay struct {
	Date            string
	ClipboardShares int
	DevicesJoined   int
}

type analyticsRangeOption struct {
	Value  string
	Label  string
	Active bool
}

type analyticsChart struct {
	HasData      bool
	YMax         int
	SharesLine   string
	JoinsLine    string
	SharesPoints []analyticsChartPoint
	JoinsPoints  []analyticsChartPoint
	Labels       []analyticsChartLabel
}

type analyticsChartPoint struct {
	X string
	Y string
}

type analyticsChartLabel struct {
	X    string
	Text string
}

func newAnalytics(now func() time.Time) *analytics {
	path := strings.TrimSpace(os.Getenv("ANALYTICS_PATH"))
	if path == "" {
		path = defaultAnalyticsPath
	}
	if path == "-" || strings.EqualFold(path, "off") {
		return nil
	}
	return &analytics{now: now, path: path}
}

func (a *app) recordAnalytics(event string) {
	if a.analytics == nil {
		return
	}
	if err := a.analytics.record(event); err != nil {
		log.Printf("analytics: %v", err)
	}
}

func (a *analytics) record(event string) error {
	if event != "device_joined" && event != "clipboard_shared" {
		return errors.New("unknown analytics event")
	}
	e := analyticsLogEvent{
		Date:  a.now().UTC().Format("2006-01-02"),
		Event: event,
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(e)
}

func (a *app) analyticsSummary(rangeValue string) (analyticsSummary, error) {
	rangeKey, _, rangeLabel := analyticsRange(rangeValue)
	if a.analytics == nil {
		return analyticsSummary{
			AnalyticsDisabled: true,
			Range:             rangeKey,
			RangeLabel:        rangeLabel,
			RangeOptions:      analyticsRangeOptions(rangeKey),
		}, nil
	}
	return a.analytics.summary(rangeValue)
}

func (a *analytics) summary(rangeValue string) (analyticsSummary, error) {
	now := a.now().UTC()
	rangeKey, rangeDays, rangeLabel := analyticsRange(rangeValue)
	s := analyticsSummary{
		GeneratedAt:  now.Format("2006-01-02 15:04 UTC"),
		Range:        rangeKey,
		RangeLabel:   rangeLabel,
		RangeOptions: analyticsRangeOptions(rangeKey),
	}
	f, err := os.Open(a.path)
	if errors.Is(err, os.ErrNotExist) {
		s.ChartDays = analyticsDaysInRange(nil, now.UTC().Truncate(24*time.Hour), rangeKey, rangeDays)
		s.Chart = buildAnalyticsChart(s.ChartDays)
		return s, nil
	}
	if err != nil {
		return s, err
	}
	defer f.Close()

	days := make(map[string]*analyticsDay)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e analyticsLogEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		event := e.Event
		if event == "" {
			event = e.Old
		}
		if event != "device_joined" && event != "clipboard_shared" {
			continue
		}
		day, ok := analyticsEventDay(e)
		if !ok {
			continue
		}
		d := days[day]
		if d == nil {
			d = &analyticsDay{Date: day}
			days[day] = d
		}
		switch event {
		case "device_joined":
			d.DevicesJoined++
		case "clipboard_shared":
			d.ClipboardShares++
		}
	}
	if err := scanner.Err(); err != nil {
		return s, err
	}

	end := now.UTC().Truncate(24 * time.Hour)
	s.ChartDays = analyticsDaysInRange(days, end, rangeKey, rangeDays)
	for _, d := range s.ChartDays {
		s.ClipboardShares += d.ClipboardShares
		s.DevicesJoined += d.DevicesJoined
		if d.ClipboardShares != 0 || d.DevicesJoined != 0 {
			s.Daily = append(s.Daily, d)
		}
	}
	sort.Slice(s.Daily, func(i, j int) bool { return s.Daily[i].Date > s.Daily[j].Date })
	s.Chart = buildAnalyticsChart(s.ChartDays)
	return s, nil
}

func analyticsEventDay(e analyticsLogEvent) (string, bool) {
	if e.Date != "" {
		if _, err := time.Parse("2006-01-02", e.Date); err == nil {
			return e.Date, true
		}
	}
	if e.Time == "" {
		return "", false
	}
	t, err := time.Parse(time.RFC3339Nano, e.Time)
	if err != nil {
		return "", false
	}
	return t.UTC().Format("2006-01-02"), true
}

func analyticsRange(value string) (key string, days int, label string) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "7":
		return "7", 7, "Last 7 days"
	case "90":
		return "90", 90, "Last 90 days"
	case "all":
		return "all", 0, "All time"
	default:
		return "30", 30, "Last 30 days"
	}
}

func analyticsRangeOptions(active string) []analyticsRangeOption {
	options := []analyticsRangeOption{
		{Value: "7", Label: "7D"},
		{Value: "30", Label: "30D"},
		{Value: "90", Label: "90D"},
		{Value: "all", Label: "All"},
	}
	for i := range options {
		options[i].Active = options[i].Value == active
	}
	return options
}

func analyticsDaysInRange(days map[string]*analyticsDay, end time.Time, rangeKey string, rangeDays int) []analyticsDay {
	start := end
	if rangeKey == "all" {
		for day := range days {
			t, err := time.Parse("2006-01-02", day)
			if err == nil && t.Before(start) {
				start = t
			}
		}
	} else {
		start = end.AddDate(0, 0, -(rangeDays - 1))
	}

	var out []analyticsDay
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		if day := days[key]; day != nil {
			out = append(out, *day)
		} else {
			out = append(out, analyticsDay{Date: key})
		}
	}
	return out
}

func buildAnalyticsChart(days []analyticsDay) analyticsChart {
	const (
		left   = 42.0
		right  = 18.0
		top    = 18.0
		bottom = 198.0
		width  = 680.0
	)
	chart := analyticsChart{}
	if len(days) == 0 {
		return chart
	}
	maxY := 1
	for _, d := range days {
		if d.ClipboardShares > maxY {
			maxY = d.ClipboardShares
		}
		if d.DevicesJoined > maxY {
			maxY = d.DevicesJoined
		}
		if d.ClipboardShares != 0 || d.DevicesJoined != 0 {
			chart.HasData = true
		}
	}
	chart.YMax = maxY
	plotW := width - left - right
	plotH := bottom - top
	denom := float64(len(days) - 1)
	for i, d := range days {
		x := left + plotW/2
		if denom > 0 {
			x = left + (float64(i) / denom * plotW)
		}
		sharesY := bottom - (float64(d.ClipboardShares) / float64(maxY) * plotH)
		joinsY := bottom - (float64(d.DevicesJoined) / float64(maxY) * plotH)
		share := analyticsChartPoint{X: fmt.Sprintf("%.1f", x), Y: fmt.Sprintf("%.1f", sharesY)}
		join := analyticsChartPoint{X: fmt.Sprintf("%.1f", x), Y: fmt.Sprintf("%.1f", joinsY)}
		chart.SharesPoints = append(chart.SharesPoints, share)
		chart.JoinsPoints = append(chart.JoinsPoints, join)
		chart.SharesLine += share.X + "," + share.Y + " "
		chart.JoinsLine += join.X + "," + join.Y + " "
	}
	for _, i := range chartLabelIndexes(len(days)) {
		d, err := time.Parse("2006-01-02", days[i].Date)
		if err != nil {
			continue
		}
		chart.Labels = append(chart.Labels, analyticsChartLabel{
			X:    chart.SharesPoints[i].X,
			Text: d.Format("Jan 2"),
		})
	}
	return chart
}

func chartLabelIndexes(n int) []int {
	if n <= 1 {
		return []int{0}
	}
	mid := n / 2
	if mid == 0 || mid == n-1 {
		return []int{0, n - 1}
	}
	return []int{0, mid, n - 1}
}
