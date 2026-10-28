package runcontext

// ProjectHistoryRunReport upgrades old presentation only for actual stored feedback.
func ProjectHistoryRunReport(view *RetainedRunHistory) (*Value, error) {
	if view == nil || view.head.Checkpoint == nil {
		return nil, &HistoryInputInvalid{}
	}
	raw, e := view.ReadObject(*view.head.Checkpoint)
	if e != nil {
		return nil, e
	}
	value, e := ParseJSON(raw)
	if e != nil {
		return nil, e
	}
	checkpoint, e := ReadExecution(value)
	if e != nil || checkpoint == nil {
		return nil, &HistoryInputInvalid{}
	}
	report, e := ProjectRunReport(*checkpoint)
	if e != nil {
		return nil, e
	}
	feedback := Array()
	for _, row := range view.Metadata() {
		if row.Record.Kind == "feedback" {
			feedback.Append(ObjectOf("sourceDigest", String(HistoryBytesDigest([]byte(historyInputText(*row.Record.Feedback)))), "record", MustParseJSON(historyInputText(*row.Record.Feedback))))
		}
	}
	if feedback.Len() == 0 {
		return report, nil
	}
	report.Set("version", Number(2))
	report.Set("checkpointVersion", Number(float64(checkpoint.Version)))
	report.Set("feedback", feedback)
	return ReadRunReport(report)
}
