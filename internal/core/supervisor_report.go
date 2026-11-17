package core

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type SupervisorReport struct {
	Number          int64
	Supervisor      string
	Session         string
	ContextRevision int
	Assessment      string
	Uncertainty     string
	Sources         []string
	Observed        []SupervisorEvidence
	CreatedAt       time.Time
}

func (c *Core) ReportSupervision(session, ref, assessment, uncertainty string, sources []string) (SupervisorReport, error) {
	view, err := c.supervisorView(ref, false)
	if err != nil {
		return SupervisorReport{}, err
	}
	if session == "" || view.Supervisor.Session == "" || c.Resolve(session) != view.Supervisor.Session {
		return SupervisorReport{}, errors.New("only the supervisor's current session records its assessment")
	}
	assessment, uncertainty = strings.TrimSpace(assessment), strings.TrimSpace(uncertainty)
	if assessment == "" || uncertainty == "" || len(sources) == 0 {
		return SupervisorReport{}, errors.New("a report needs assessment, uncertainty and primary evidence sources")
	}
	for i, source := range sources {
		sources[i] = strings.TrimSpace(source)
		if sources[i] == "" {
			return SupervisorReport{}, errors.New("an evidence source cannot be empty")
		}
	}
	report := SupervisorReport{Supervisor: view.Supervisor.ID, Session: session, ContextRevision: view.Revision, Assessment: assessment, Uncertainty: uncertainty, Sources: sources, Observed: view.Evidence, CreatedAt: c.now()}
	raw, err := json.Marshal(report)
	if err != nil {
		return report, err
	}
	err = c.Store.db.QueryRow(`INSERT INTO ym_supervisor_reports(supervisor,body) VALUES (?,?) RETURNING num`, report.Supervisor, string(raw)).Scan(&report.Number)
	return report, err
}

func (s *Store) SupervisorReports(supervisor string) ([]SupervisorReport, error) {
	rows, err := s.db.Query(`SELECT num,body FROM ym_supervisor_reports WHERE supervisor=? ORDER BY num`, supervisor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reports := []SupervisorReport{}
	for rows.Next() {
		var number int64
		var raw string
		if err := rows.Scan(&number, &raw); err != nil {
			return nil, err
		}
		var report SupervisorReport
		if err := json.Unmarshal([]byte(raw), &report); err != nil {
			return nil, err
		}
		report.Number = number
		reports = append(reports, report)
	}
	return reports, rows.Err()
}

func (c *Core) RememberSupervision(session, ref, text, source string) (Steer, error) {
	view, err := c.supervisorView(ref, false)
	if err != nil {
		return Steer{}, err
	}
	if session == "" || view.Supervisor.Session == "" || c.Resolve(session) != view.Supervisor.Session {
		return Steer{}, errors.New("only the supervisor's current session records received context")
	}
	text, source = strings.TrimSpace(text), strings.TrimSpace(source)
	if text == "" || source == "" {
		return Steer{}, errors.New("received context needs the correction and its source")
	}
	b, err := c.Board()
	if err != nil {
		return Steer{}, err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return Steer{}, err
	}
	return c.applySteer(b, o, view.Supervisor, view.Context+"\n\nReceived correction (recorded by supervisor; source: "+source+"):\n"+text, view.Supervisor.ID, "")
}
