package main

import (
	"strings"
	"testing"
)

func TestFastThinkingRequestPairs(t *testing.T) {
	models := list(providerSpec()["models"])
	if len(models) == 0 {
		t.Fatal("no bundled models")
	}
	for _, value := range models {
		model := obj(value)
		t.Run(str(model["id"]), func(t *testing.T) {
			if model["fastServiceTier"] != "priority" {
				t.Fatal("bundled model lacks priority capability")
			}
			mapping := obj(model["thinkingLevelMap"])
			for _, base := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
				mapped, declared := mapping[base]
				if declared && mapped == nil {
					continue
				}
				for _, fast := range []bool{false, true} {
					level := base
					if fast {
						level += " fast"
					}
					t.Run(level, func(t *testing.T) {
						payload := basicPayload("")
						payload["model"] = model
						req := obj(payload["request"])
						req["thinkingEffort"] = level
						req["thinkingLevelMap"] = mapping
						for _, compact := range []bool{false, true} {
							build := buildRequest
							if compact {
								build = buildCompactRequest
							}
							body, err := build(payload)
							if err != nil {
								t.Fatal(err)
							}
							if fast && body["service_tier"] != "priority" {
								t.Fatalf("fast tier: %v", body["service_tier"])
							}
							if !fast {
								if _, exists := body["service_tier"]; exists {
									t.Fatal("normal thinking must omit tier")
								}
							}
							if base == "off" {
								if _, exists := body["reasoning"]; exists {
									t.Fatal("off must omit reasoning even with Fast")
								}
							} else {
								want := mapped
								if !declared {
									want = base
								}
								if obj(body["reasoning"])["effort"] != want {
									t.Fatalf("reasoning: %v, want %v", body["reasoning"], want)
								}
							}
							if compact {
								input := list(body["input"])
								if obj(input[len(input)-1])["type"] != "compaction_trigger" {
									t.Fatal("missing compaction trigger")
								}
							}
						}
					})
				}
			}
		})
	}
}

func TestFastThinkingOffAndCapability(t *testing.T) {
	payload := basicPayload("")
	obj(payload["model"])["fastServiceTier"] = "priority"
	request := obj(payload["request"])
	request["thinkingEffort"] = "off fast"
	request["thinkingLevelMap"] = object{"off": "none"}
	body, err := buildRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if body["service_tier"] != "priority" || body["reasoning"] != nil {
		t.Fatalf("off fast: %v", body)
	}
	delete(obj(payload["model"]), "fastServiceTier")
	for _, build := range []func(object) (object, error){buildRequest, buildCompactRequest} {
		if _, err := build(payload); err == nil || !strings.Contains(err.Error(), "does not support Fast") {
			t.Fatalf("unsupported Fast should fail: %v", err)
		}
	}
}
