package main

import (
	"context"
	"encoding/json"
	"math"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const PluginName = "budget-pressure"
const PluginVersion = "0.1.0"

// Plugin turns the requesting user's remaining budget, as resolved by the
// host, into a pressure between 0 (budget untouched) and 1 (budget exhausted).
// The overall pressure is the worst of the daily, monthly and yearly periods,
// each of which is also exposed for finer rules. Without any configured
// budget the pressure is 0.
type Plugin struct {
	proto.UnimplementedXoloPluginServer
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Mesure la pression budgétaire de l'utilisateur (part du budget déjà consommée, entre 0 et 1) sur les périodes jour, mois et année.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "budget_pressure", PortType: "number"},
			{Name: "daily_pressure", PortType: "number"},
			{Name: "monthly_pressure", PortType: "number"},
			{Name: "yearly_pressure", PortType: "number"},
			{Name: "has_budget", PortType: "boolean"},
		},
	}, nil
}

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	q := in.GetQuota()

	daily := pressure(q.GetDailyTotal(), q.GetDailyRemaining())
	monthly := pressure(q.GetMonthlyTotal(), q.GetMonthlyRemaining())
	yearly := pressure(q.GetYearlyTotal(), q.GetYearlyRemaining())

	outputs := map[string]interface{}{
		"budget_pressure":  math.Max(daily, math.Max(monthly, yearly)),
		"daily_pressure":   daily,
		"monthly_pressure": monthly,
		"yearly_pressure":  yearly,
		"has_budget":       q.GetDailyTotal() > 0 || q.GetMonthlyTotal() > 0 || q.GetYearlyTotal() > 0,
	}
	b, _ := json.Marshal(outputs)

	return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(b)}, nil
}

// pressure returns the consumed share of a budget, clamped to [0, 1]. A period
// without budget (total <= 0) is unlimited and exerts no pressure.
func pressure(total, remaining float64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Max(0, math.Min(1, 1-remaining/total))
}
