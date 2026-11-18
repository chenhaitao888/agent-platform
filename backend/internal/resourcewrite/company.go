package resourcewrite

import (
	"context"
	"time"

	"agent-platform/backend/internal/planning"
)

// CompanyWriter saves changes, submits company work orders and observes the
// company's automatic publication. It deliberately has no Approve or Publish.
type CompanyWriter interface {
	Writer
	Submit(context.Context, Intent, Receipt, string) (CompanyTicket, error)
	LookupTicket(context.Context, Intent, Receipt, string) (CompanyTicket, error)
	Observe(context.Context, Intent, Receipt, *CompanyTicket) (CompanyObservation, error)
}

const CompanyImplementation = "mcp-company.v1"

type CompanyChange struct {
	ID      string         `json:"id"`
	Version string         `json:"version"`
	Ticket  *CompanyTicket `json:"ticket,omitempty"`
}

type CompanyTicket struct {
	ID            string          `json:"id"`
	RequestID     string          `json:"requestId"`
	ChangeID      string          `json:"changeId"`
	ChangeVersion string          `json:"changeVersion"`
	Target        planning.Target `json:"target"`
	State         string          `json:"state"`
	URL           string          `json:"url,omitempty"`
}

func (t CompanyTicket) Matches(i Intent, r Receipt, requestID string) bool {
	return r.Company != nil && planning.ValidIdentifier(t.ID) && t.RequestID == requestID && t.ChangeID == r.Company.ID && t.ChangeVersion == r.Company.Version && t.Target == i.Target
}

type CompanyPublication struct {
	ID               string          `json:"id"`
	TicketID         string          `json:"ticketId"`
	ChangeID         string          `json:"changeId"`
	ChangeVersion    string          `json:"changeVersion"`
	Target           planning.Target `json:"target"`
	State            string          `json:"state"`
	EffectiveVersion string          `json:"effectiveVersion,omitempty"`
}

type PublishedConfig struct {
	Target  planning.Target `json:"target"`
	Key     string          `json:"key"`
	Version string          `json:"version"`
	Value   planning.Value  `json:"value"`
}

type CompanyObservation struct {
	State            string              `json:"state"`
	Ticket           *CompanyTicket      `json:"ticket,omitempty"`
	Publication      *CompanyPublication `json:"publication,omitempty"`
	EffectiveVersion string              `json:"effectiveVersion,omitempty"`
	ValueHash        string              `json:"valueHash,omitempty"`
	ObservedAt       time.Time           `json:"observedAt"`
}
