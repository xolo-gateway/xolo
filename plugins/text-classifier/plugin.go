package main

import (
	"context"
	"encoding/json"
	"sync"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"github.com/xolo-gateway/xolo/plugins/internal/complexity"
	"github.com/xolo-gateway/xolo/plugins/internal/complexity/data"
	"github.com/xolo-gateway/xolo/plugins/internal/requesttext"
)

const PluginName = "text-classifier"
const PluginVersion = "0.1.0"

// Plugin classifies the request into a thematic category (code, writing,
// analysis…) with an embedded Naive Bayes model. The model is loaded once per
// plugin process.
type Plugin struct {
	proto.UnimplementedXoloPluginServer

	once    sync.Once
	model   *complexity.NaiveBayes
	loadErr error
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         PluginName,
		Version:      PluginVersion,
		Description:  "Classe la requête dans une catégorie thématique (code, rédaction, analyse…) à l'aide d'un modèle bayésien embarqué.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_PRE_REQUEST},
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "category", PortType: "string"},
			{Name: "confidence", PortType: "number"},
		},
	}, nil
}

func (p *Plugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	category, confidence := "unknown", 0.0

	nb := p.classifier()
	text := requesttext.Prompt(in.MessagesJson)
	if nb != nil && text != "" {
		pred := nb.Predict(text)
		category, confidence = pred.Class, pred.Prob
	}

	b, _ := json.Marshal(map[string]interface{}{
		"category":   category,
		"confidence": confidence,
	})
	return &proto.PreRequestOutput{Allowed: true, OutputsJson: string(b)}, nil
}

func (p *Plugin) classifier() *complexity.NaiveBayes {
	p.once.Do(func() {
		p.model, p.loadErr = complexity.LoadModel(data.RawModel)
	})
	if p.loadErr != nil {
		return nil
	}
	return p.model
}
