package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// cfgBatchGroupLimits stores which channels."group" values batch-create may
// write, per channel type, as JSON {type: {default: [...], studios: {studio:
// [...]}}}. A studio with its own non-empty list uses it; everyone else uses
// the type's default list. A type with no rule (or an empty resolved list) is
// unrestricted — the panel's free-text group field keeps working as before.
const cfgBatchGroupLimits = "batch_create_group_limits"

type batchGroupRule struct {
	Default []string            `json:"default"`
	Studios map[string][]string `json:"studios"`
}

func loadBatchGroupLimits() map[int]batchGroupRule {
	var raw string
	if err := db.QueryRow(`SELECT value FROM report_config WHERE key=$1`, cfgBatchGroupLimits).Scan(&raw); err != nil {
		return map[int]batchGroupRule{}
	}
	out := map[int]batchGroupRule{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[int]batchGroupRule{}
	}
	return out
}

// allowedBatchGroups resolves the group allowlist for a (preset type, studio)
// pair. nil means unrestricted.
func allowedBatchGroups(limits map[int]batchGroupRule, channelType int, studio string) []string {
	rule, ok := limits[channelType]
	if !ok {
		return nil
	}
	if groups := rule.Studios[studio]; len(groups) > 0 {
		return groups
	}
	if len(rule.Default) > 0 {
		return rule.Default
	}
	return nil
}

// normalizeGroupList splits comma/whitespace-separated entries, trims, and
// de-duplicates while keeping the first-seen order.
func normalizeGroupList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, item := range in {
		for _, g := range strings.FieldsFunc(item, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
		}) {
			if !slices.Contains(out, g) {
				out = append(out, g)
			}
		}
	}
	return out
}

func handleGetBatchGroupLimits(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"limits": loadBatchGroupLimits()})
}

func handleSetBatchGroupLimits(c *gin.Context) {
	var body struct {
		Limits map[int]batchGroupRule `json:"limits"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Normalize: drop blank studios / empty lists so the stored map only
	// carries real restrictions. Type allowlist matches handleBatchCreateChannels.
	clean := map[int]batchGroupRule{}
	for channelType, rule := range body.Limits {
		switch channelType {
		case 1, 3, 14, 20, 24, 33, 41, openRouterOpenAIPresetType:
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unsupported channel type %d", channelType)})
			return
		}
		next := batchGroupRule{Default: normalizeGroupList(rule.Default), Studios: map[string][]string{}}
		for studio, groups := range rule.Studios {
			studio = strings.TrimSpace(studio)
			groups = normalizeGroupList(groups)
			if studio == "" || len(groups) == 0 {
				continue
			}
			next.Studios[studio] = groups
		}
		if len(next.Default) == 0 && len(next.Studios) == 0 {
			continue
		}
		clean[channelType] = next
	}
	blob, err := json.Marshal(clean)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := writeLocalPoolConfig(cfgBatchGroupLimits, string(blob)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"limits": clean})
}

// handleBatchAllowedGroups returns the group allowlist the batch-create panel
// should offer for ?type=&studio=. Studio operators are pinned to their bound
// studio. Empty list = unrestricted (free-text group).
func handleBatchAllowedGroups(c *gin.Context) {
	channelType, _ := strconv.Atoi(c.Query("type"))
	if channelType == 0 {
		channelType = 14
	}
	studio := strings.TrimSpace(c.Query("studio"))
	if callerIsStudioOperator(c) {
		studio = callerStudio(c)
	}
	groups := allowedBatchGroups(loadBatchGroupLimits(), channelType, studio)
	if groups == nil {
		groups = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"groups": groups})
}
