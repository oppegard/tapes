package deck

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/papercomputeco/tapes/pkg/llm"
)

type queryTestEnv struct {
	ctx context.Context
	q   *Query
}

func newQueryTestEnv() *queryTestEnv {
	ctx := context.Background()
	dbPath := filepath.Join(GinkgoT().TempDir(), "tapes-query-test.db")
	q, closeFn, err := NewQuery(ctx, dbPath, DefaultPricing())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		Expect(closeFn()).To(Succeed())
	})
	return &queryTestEnv{ctx: ctx, q: q}
}

type nodeFixture struct {
	id         string
	parentHash string
	role       string
	model      string
	provider   string
	stopReason string
	createdAt  time.Time
	content    []llm.ContentBlock
}

func insertNode(env *queryTestEnv, fx nodeFixture) {
	builder := env.q.client.Node.Create().
		SetID(fx.id).
		SetCreatedAt(fx.createdAt)
	if fx.parentHash != "" {
		builder.SetParentHash(fx.parentHash)
	}
	if fx.role != "" {
		builder.SetRole(fx.role)
	}
	if fx.model != "" {
		builder.SetModel(fx.model)
	}
	if fx.provider != "" {
		builder.SetProvider(fx.provider)
	}
	if fx.stopReason != "" {
		builder.SetStopReason(fx.stopReason)
	}
	if len(fx.content) > 0 {
		builder.SetContent(rawBlocks(fx.content))
	}
	Expect(builder.Exec(env.ctx)).To(Succeed())
}

func rawBlocks(blocks []llm.ContentBlock) []map[string]any {
	if len(blocks) == 0 {
		return nil
	}
	data, err := json.Marshal(blocks)
	Expect(err).NotTo(HaveOccurred())
	var raw []map[string]any
	Expect(json.Unmarshal(data, &raw)).To(Succeed())
	return raw
}

func metricByName(metrics []ToolMetric, name string) (ToolMetric, bool) {
	for _, metric := range metrics {
		if metric.Name == name {
			return metric, true
		}
	}
	return ToolMetric{}, false
}

var _ = Describe("Query cache and index behavior", func() {
	It("caches Overview results within TTL and refreshes after expiration", func() {
		env := newQueryTestEnv()
		base := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

		insertNode(env, nodeFixture{
			id:         "session-a",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base,
			content: []llm.ContentBlock{{
				Type: "text",
				Text: "session a",
			}},
		})

		overviewA, err := env.q.Overview(env.ctx, Filters{})
		Expect(err).NotTo(HaveOccurred())
		Expect(overviewA.Sessions).To(HaveLen(1))

		insertNode(env, nodeFixture{
			id:         "session-b",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base.Add(2 * time.Minute),
			content: []llm.ContentBlock{{
				Type: "text",
				Text: "session b",
			}},
		})

		overviewCached, err := env.q.Overview(env.ctx, Filters{})
		Expect(err).NotTo(HaveOccurred())
		Expect(overviewCached.Sessions).To(HaveLen(1))

		env.q.cache.loadedAt = time.Now().Add(-sessionCacheTTL - time.Second)
		overviewRefreshed, err := env.q.Overview(env.ctx, Filters{})
		Expect(err).NotTo(HaveOccurred())
		Expect(overviewRefreshed.Sessions).To(HaveLen(2))
	})

	It("caches AnalyticsOverview results within TTL and refreshes after expiration", func() {
		env := newQueryTestEnv()
		base := time.Date(2026, 2, 24, 11, 0, 0, 0, time.UTC)

		insertNode(env, nodeFixture{
			id:         "analytics-a",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base,
			content:    []llm.ContentBlock{{Type: "text", Text: "analytics a"}},
		})

		analyticsA, err := env.q.AnalyticsOverview(env.ctx, Filters{})
		Expect(err).NotTo(HaveOccurred())
		Expect(analyticsA.TotalSessions).To(Equal(1))

		insertNode(env, nodeFixture{
			id:         "analytics-b",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base.Add(3 * time.Minute),
			content:    []llm.ContentBlock{{Type: "text", Text: "analytics b"}},
		})

		analyticsCached, err := env.q.AnalyticsOverview(env.ctx, Filters{})
		Expect(err).NotTo(HaveOccurred())
		Expect(analyticsCached.TotalSessions).To(Equal(1))

		env.q.cache.loadedAt = time.Now().Add(-sessionCacheTTL - time.Second)
		analyticsRefreshed, err := env.q.AnalyticsOverview(env.ctx, Filters{})
		Expect(err).NotTo(HaveOccurred())
		Expect(analyticsRefreshed.TotalSessions).To(Equal(2))
	})

	It("uses cached by-ID fast path for SessionDetail when leaf row is removed", func() {
		env := newQueryTestEnv()
		base := time.Date(2026, 2, 24, 12, 0, 0, 0, time.UTC)

		insertNode(env, nodeFixture{
			id:         "detail-fast",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base,
			content:    []llm.ContentBlock{{Type: "text", Text: "fast path"}},
		})

		_, err := env.q.Overview(env.ctx, Filters{}) // warm cache
		Expect(err).NotTo(HaveOccurred())
		Expect(env.q.client.Node.DeleteOneID("detail-fast").Exec(env.ctx)).To(Succeed())

		detail, err := env.q.SessionDetail(env.ctx, "detail-fast")
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Summary.ID).To(Equal("detail-fast"))
		Expect(detail.Messages).To(HaveLen(1))
	})

	It("reloads candidates on stale cache and finds newly inserted session in SessionDetail", func() {
		env := newQueryTestEnv()
		base := time.Date(2026, 2, 24, 13, 0, 0, 0, time.UTC)

		insertNode(env, nodeFixture{
			id:         "detail-a",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base,
			content:    []llm.ContentBlock{{Type: "text", Text: "detail a"}},
		})

		_, err := env.q.Overview(env.ctx, Filters{}) // warm cache
		Expect(err).NotTo(HaveOccurred())

		insertNode(env, nodeFixture{
			id:         "detail-b",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base.Add(2 * time.Minute),
			content:    []llm.ContentBlock{{Type: "text", Text: "detail b"}},
		})

		env.q.cache.loadedAt = time.Now().Add(-sessionCacheTTL - time.Second)
		detail, err := env.q.SessionDetail(env.ctx, "detail-b")
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Summary.ID).To(Equal("detail-b"))
	})

	It("falls back to direct ancestry lookup for SessionDetail IDs not in candidate set", func() {
		env := newQueryTestEnv()
		base := time.Date(2026, 2, 24, 14, 0, 0, 0, time.UTC)

		insertNode(env, nodeFixture{
			id:        "root-session",
			role:      roleUser,
			model:     "gpt-4o",
			provider:  "openai",
			createdAt: base,
			content:   []llm.ContentBlock{{Type: "text", Text: "root prompt"}},
		})
		insertNode(env, nodeFixture{
			id:         "leaf-session",
			parentHash: "root-session",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base.Add(time.Minute),
			content:    []llm.ContentBlock{{Type: "text", Text: "leaf response"}},
		})

		detail, err := env.q.SessionDetail(env.ctx, "root-session")
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Summary.ID).To(Equal("root-session"))
		Expect(detail.Messages).To(HaveLen(1))
		Expect(detail.Messages[0].Hash).To(Equal("root-session"))
	})

	It("maintains SessionAnalytics fast path and fallback behavior", func() {
		env := newQueryTestEnv()
		base := time.Date(2026, 2, 24, 15, 0, 0, 0, time.UTC)

		insertNode(env, nodeFixture{
			id:         "analytics-fast",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base,
			content:    []llm.ContentBlock{{Type: "text", Text: "analytics fast"}},
		})
		insertNode(env, nodeFixture{
			id:        "analytics-root",
			role:      roleUser,
			model:     "gpt-4o",
			provider:  "openai",
			createdAt: base.Add(2 * time.Minute),
			content:   []llm.ContentBlock{{Type: "text", Text: "root only"}},
		})
		insertNode(env, nodeFixture{
			id:         "analytics-leaf",
			parentHash: "analytics-root",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base.Add(3 * time.Minute),
			content:    []llm.ContentBlock{{Type: "text", Text: "child"}},
		})

		_, err := env.q.Overview(env.ctx, Filters{}) // warm cache
		Expect(err).NotTo(HaveOccurred())
		Expect(env.q.client.Node.DeleteOneID("analytics-fast").Exec(env.ctx)).To(Succeed())

		fast, err := env.q.SessionAnalytics(env.ctx, "analytics-fast")
		Expect(err).NotTo(HaveOccurred())
		Expect(fast.SessionID).To(Equal("analytics-fast"))

		fallback, err := env.q.SessionAnalytics(env.ctx, "analytics-root")
		Expect(err).NotTo(HaveOccurred())
		Expect(fallback.SessionID).To(Equal("analytics-root"))
	})

	It("stores cache candidates by ID on copied data and invalidates when stale", func() {
		q := &Query{}
		input := []sessionCandidate{
			{summary: SessionSummary{ID: "copy-a"}},
			{summary: SessionSummary{ID: "copy-b"}},
		}

		q.storeSessionCandidates(input)
		input[0].summary.ID = "mutated"

		cached := q.cachedSessionCandidate("copy-a")
		Expect(cached).NotTo(BeNil())
		Expect(cached.summary.ID).To(Equal("copy-a"))

		all := q.cachedSessionCandidates()
		Expect(all).To(HaveLen(2))
		all[0].summary.ID = "edited-copy"

		again := q.cachedSessionCandidate("copy-a")
		Expect(again).NotTo(BeNil())
		Expect(again.summary.ID).To(Equal("copy-a"))

		q.cache.loadedAt = time.Now().Add(-sessionCacheTTL - time.Second)
		Expect(q.cachedSessionCandidate("copy-a")).To(BeNil())
		Expect(q.cachedSessionCandidates()).To(BeNil())
	})

	It("attributes tool errors to each tool call in AnalyticsOverview", func() {
		env := newQueryTestEnv()
		base := time.Date(2026, 2, 24, 16, 0, 0, 0, time.UTC)

		insertNode(env, nodeFixture{
			id:         "tool-errors",
			role:       roleAssistant,
			model:      "gpt-4o",
			provider:   "openai",
			stopReason: "stop",
			createdAt:  base,
			content: []llm.ContentBlock{
				{Type: "tool_use", ToolName: "Read", ToolInput: map[string]any{"path": "README.md"}},
				{Type: "tool_use", ToolName: "Bash", ToolInput: map[string]any{"command": "ls"}},
				{Type: "tool_result", ToolResultID: "tool-1", ToolOutput: "failed", IsError: true},
			},
		})

		analytics, err := env.q.AnalyticsOverview(env.ctx, Filters{})
		Expect(err).NotTo(HaveOccurred())
		Expect(analytics.TotalSessions).To(Equal(1))

		readMetric, ok := metricByName(analytics.TopTools, "Read")
		Expect(ok).To(BeTrue())
		Expect(readMetric.Count).To(Equal(1))
		Expect(readMetric.ErrorCount).To(Equal(1))
		Expect(readMetric.Sessions).To(Equal(1))

		bashMetric, ok := metricByName(analytics.TopTools, "Bash")
		Expect(ok).To(BeTrue())
		Expect(bashMetric.Count).To(Equal(1))
		Expect(bashMetric.ErrorCount).To(Equal(1))
		Expect(bashMetric.Sessions).To(Equal(1))
	})
})
