package layers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
	"github.com/runatlantis/atlantis/server/logging"
)

// fakeVCSClient is a simple test double for vcs.Client.
// It only implements the methods needed for DashboardUpdater tests.
type fakeVCSClient struct {
	vcs.Client // embed to satisfy interface; only override what we need

	comments        []vcs.PullComment
	listErr         error
	createdComments []string
	editedComments  map[int64]string
	editErr         error
	maxLen          int
}

func newFakeVCSClient() *fakeVCSClient {
	return &fakeVCSClient{
		editedComments: make(map[int64]string),
		maxLen:         65536,
	}
}

func (f *fakeVCSClient) ListComments(_ logging.SimpleLogging, _ models.Repo, _ int) ([]vcs.PullComment, error) {
	return f.comments, f.listErr
}

func (f *fakeVCSClient) EditComment(_ logging.SimpleLogging, _ models.Repo, _ int, commentID int64, body string) error {
	if f.editErr != nil {
		return f.editErr
	}
	f.editedComments[commentID] = body
	return nil
}

func (f *fakeVCSClient) CreateComment(_ logging.SimpleLogging, _ models.Repo, _ int, comment string, _ string) error {
	f.createdComments = append(f.createdComments, comment)
	return nil
}

func (f *fakeVCSClient) MaxCommentLength() int {
	return f.maxLen
}

func testLogger(t *testing.T) logging.SimpleLogging {
	t.Helper()
	return logging.NewNoopLogger(t)
}

func testRepo() models.Repo {
	return models.Repo{
		FullName: "owner/repo",
		Owner:    "owner",
		Name:     "repo",
	}
}

func simpleData() DashboardData {
	return DashboardData{
		Layers: []DashboardLayer{
			{
				Number:    1,
				IsCurrent: true,
				Projects: []DashboardProject{
					{Name: "vpc", Status: NewDashboardStatus(models.PendingPlanStatus)},
				},
			},
		},
	}
}

func TestUpdate_CreateNew(t *testing.T) {
	client := newFakeVCSClient()
	updater := &DashboardUpdater{
		VCSClient: client,
		Renderer:  NewDashboardRenderer(""),
	}

	err := updater.Update(testLogger(t), testRepo(), 1, simpleData())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(client.createdComments) != 1 {
		t.Fatalf("expected 1 created comment, got %d", len(client.createdComments))
	}
	if !strings.Contains(client.createdComments[0], DashboardSentinel) {
		t.Error("created comment should contain sentinel")
	}
}

func TestUpdate_UpdateExisting(t *testing.T) {
	client := newFakeVCSClient()
	client.comments = []vcs.PullComment{
		{
			ID:   100,
			Body: DashboardSentinel + "\nold content",
		},
	}
	updater := &DashboardUpdater{
		VCSClient: client,
		Renderer:  NewDashboardRenderer(""),
	}

	err := updater.Update(testLogger(t), testRepo(), 1, simpleData())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(client.createdComments) != 0 {
		t.Errorf("expected 0 created comments, got %d", len(client.createdComments))
	}
	if body, ok := client.editedComments[100]; !ok {
		t.Error("expected comment 100 to be edited")
	} else if !strings.Contains(body, DashboardSentinel) {
		t.Error("edited comment should contain sentinel")
	}
}

func TestUpdate_ShrinkDashboard(t *testing.T) {
	client := newFakeVCSClient()
	client.comments = []vcs.PullComment{
		{ID: 100, Body: DashboardSentinel + "\npart 1"},
		{ID: 101, Body: fmt.Sprintf(DashboardContinuedSentinelFmt, 2) + "\npart 2"},
		{ID: 102, Body: fmt.Sprintf(DashboardContinuedSentinelFmt, 3) + "\npart 3"},
	}
	updater := &DashboardUpdater{
		VCSClient: client,
		Renderer:  NewDashboardRenderer(""),
	}

	// Simple data fits in 1 comment
	err := updater.Update(testLogger(t), testRepo(), 1, simpleData())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Comment 100 should be updated with new content
	if _, ok := client.editedComments[100]; !ok {
		t.Error("expected comment 100 to be edited")
	}

	// Comments 101 and 102 should be cleared
	for _, id := range []int64{101, 102} {
		body, ok := client.editedComments[id]
		if !ok {
			t.Errorf("expected comment %d to be edited (cleared)", id)
			continue
		}
		if !strings.Contains(body, "no longer needed") {
			t.Errorf("expected comment %d to be cleared with 'no longer needed', got: %s", id, body)
		}
	}
}

func TestUpdate_EditFails_FallsBackToCreate(t *testing.T) {
	client := newFakeVCSClient()
	client.comments = []vcs.PullComment{
		{ID: 100, Body: DashboardSentinel + "\nold"},
	}
	client.editErr = fmt.Errorf("permission denied")
	updater := &DashboardUpdater{
		VCSClient: client,
		Renderer:  NewDashboardRenderer(""),
	}

	err := updater.Update(testLogger(t), testRepo(), 1, simpleData())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have fallen back to creating a new comment
	if len(client.createdComments) != 1 {
		t.Errorf("expected 1 created comment (fallback), got %d", len(client.createdComments))
	}
}

func TestUpdate_ListCommentsFails(t *testing.T) {
	client := newFakeVCSClient()
	client.listErr = fmt.Errorf("network error")
	updater := &DashboardUpdater{
		VCSClient: client,
		Renderer:  NewDashboardRenderer(""),
	}

	err := updater.Update(testLogger(t), testRepo(), 1, simpleData())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should create new comment when listing fails
	if len(client.createdComments) != 1 {
		t.Errorf("expected 1 created comment, got %d", len(client.createdComments))
	}
}

func TestIsDashboardComment_Sentinel(t *testing.T) {
	body := DashboardSentinel + "\n## Dashboard\nsome content"
	if !isDashboardComment(body) {
		t.Error("expected body with sentinel to be identified as dashboard comment")
	}
}

func TestIsDashboardComment_ContinuedSentinel(t *testing.T) {
	body := fmt.Sprintf(DashboardContinuedSentinelFmt, 2) + "\nsome content"
	if !isDashboardComment(body) {
		t.Error("expected body with continued sentinel to be identified as dashboard comment")
	}
}

func TestIsDashboardComment_NormalComment(t *testing.T) {
	body := "Just a normal atlantis plan output comment"
	if isDashboardComment(body) {
		t.Error("expected normal comment to NOT be identified as dashboard comment")
	}
}

func TestUpdate_SplitAndUpdate(t *testing.T) {
	// Set up a fake client with 1 existing dashboard comment.
	client := newFakeVCSClient()
	client.comments = []vcs.PullComment{
		{ID: 200, Body: DashboardSentinel + "\nold single comment"},
	}

	renderer := NewDashboardRenderer("")

	// Build dashboard data with 2 layers, each having many projects, so
	// the rendered output is large enough to require splitting.
	var layer1Projects []DashboardProject
	var layer2Projects []DashboardProject
	for i := 0; i < 20; i++ {
		layer1Projects = append(layer1Projects, DashboardProject{
			Name:   fmt.Sprintf("layer1-project-%03d", i),
			Status: NewDashboardStatus(models.PlannedPlanStatus),
		})
		layer2Projects = append(layer2Projects, DashboardProject{
			Name:   fmt.Sprintf("layer2-project-%03d", i),
			Status: NewDashboardStatus(models.PendingPlanStatus),
		})
	}

	data := DashboardData{
		Layers: []DashboardLayer{
			{Number: 1, IsCurrent: true, Projects: layer1Projects},
			{Number: 2, Projects: layer2Projects},
		},
	}

	// Pre-render to find a maxLen that forces splitting into multiple parts.
	fullRender := renderer.Render(data)
	// Set maxLen to roughly 2/3 of the full render to force at least 2 parts.
	splitLen := len(fullRender) * 2 / 3
	client.maxLen = splitLen

	updater := &DashboardUpdater{
		VCSClient: client,
		Renderer:  renderer,
	}

	err := updater.Update(testLogger(t), testRepo(), 1, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The existing comment (ID 200) should be edited with part 1.
	body1, ok := client.editedComments[200]
	if !ok {
		t.Fatal("expected existing comment 200 to be edited with part 1")
	}
	if !strings.Contains(body1, DashboardSentinel) {
		t.Error("part 1 should contain the primary sentinel")
	}

	// New comments should be created for the remaining parts (at least 1).
	if len(client.createdComments) < 1 {
		t.Fatalf("expected at least 1 new comment created for overflow parts, got %d", len(client.createdComments))
	}
	// Each created comment should have a continued sentinel.
	for i, comment := range client.createdComments {
		if !strings.Contains(comment, "continued") {
			t.Errorf("created comment %d should contain a continued sentinel", i+1)
		}
	}
}

func TestFindDashboardComments_SortsBySentinelNumber(t *testing.T) {
	// Simulate comments returned out of order
	client := newFakeVCSClient()
	client.comments = []vcs.PullComment{
		{ID: 300, Body: fmt.Sprintf(DashboardContinuedSentinelFmt, 3) + "\npart 3"},
		{ID: 100, Body: DashboardSentinel + "\npart 1"},
		{ID: 200, Body: fmt.Sprintf(DashboardContinuedSentinelFmt, 2) + "\npart 2"},
	}
	u := &DashboardUpdater{VCSClient: client}

	result, err := u.findDashboardComments(testLogger(t), testRepo(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be sorted: primary (1), continued 2, continued 3
	if len(result) != 3 {
		t.Fatalf("expected 3 comments, got %d", len(result))
	}
	if result[0].ID != 100 {
		t.Errorf("expected first comment ID 100 (primary), got %d", result[0].ID)
	}
	if result[1].ID != 200 {
		t.Errorf("expected second comment ID 200 (continued 2), got %d", result[1].ID)
	}
	if result[2].ID != 300 {
		t.Errorf("expected third comment ID 300 (continued 3), got %d", result[2].ID)
	}
}

func TestUpdate_SplitWorkflow_Integration(t *testing.T) {
	client := newFakeVCSClient()
	// Use a small maxLen to force splitting
	client.maxLen = 500
	renderer := NewDashboardRenderer("")

	// Create data that will require splitting: 2 layers with many projects
	var layer1Projects, layer2Projects []DashboardProject
	for i := 0; i < 25; i++ {
		layer1Projects = append(layer1Projects, DashboardProject{
			Name:   fmt.Sprintf("project-%02d", i),
			Status: NewDashboardStatus(models.PlannedPlanStatus),
		})
		layer2Projects = append(layer2Projects, DashboardProject{
			Name:   fmt.Sprintf("project-%02d", i+25),
			Status: NewDashboardStatus(models.PendingPlanStatus),
		})
	}

	data := DashboardData{
		Layers: []DashboardLayer{
			{Number: 1, IsCurrent: false, Projects: layer1Projects},
			{Number: 2, IsCurrent: true, Projects: layer2Projects},
		},
	}

	updater := &DashboardUpdater{
		VCSClient: client,
		Renderer:  renderer,
	}

	// --- First update: creates multiple comments ---
	err := updater.Update(testLogger(t), testRepo(), 1, data)
	if err != nil {
		t.Fatalf("first update failed: %v", err)
	}

	initialCount := len(client.createdComments)
	if initialCount < 2 {
		t.Fatalf("expected at least 2 comments created, got %d", initialCount)
	}

	// All parts should be within maxLen
	for i, part := range client.createdComments {
		if len(part) > client.maxLen {
			t.Errorf("part %d exceeds maxLen: %d > %d", i+1, len(part), client.maxLen)
		}
	}

	// Save the first render for idempotency check
	firstRender := make([]string, len(client.createdComments))
	copy(firstRender, client.createdComments)

	// --- Second update: simulate existing comments, should update in place ---
	// Convert created comments into existing pull comments with IDs
	client.comments = nil
	for i, body := range firstRender {
		client.comments = append(client.comments, vcs.PullComment{
			ID:   int64(100 + i),
			Body: body,
		})
	}
	client.createdComments = nil
	client.editedComments = make(map[int64]string)

	err = updater.Update(testLogger(t), testRepo(), 1, data)
	if err != nil {
		t.Fatalf("second update failed: %v", err)
	}

	// Should have edited existing comments, not created new ones
	if len(client.createdComments) > 0 {
		t.Errorf("expected edits only on re-render, but got %d new comments", len(client.createdComments))
	}
	if len(client.editedComments) != initialCount {
		t.Errorf("expected %d edits, got %d", initialCount, len(client.editedComments))
	}

	// Content should be identical on re-render (idempotent)
	if body, ok := client.editedComments[100]; ok {
		if body != firstRender[0] {
			t.Error("first comment content should be identical on re-render (idempotent)")
		}
	}

	// --- Verify all content is preserved (no truncation) ---
	allContent := strings.Join(firstRender, "\n")
	if strings.Contains(allContent, "truncated") {
		t.Error("should not truncate, should split mid-layer instead")
	}

	// Layer 2 should be present somewhere in the output
	if !strings.Contains(allContent, "Layer 2") {
		t.Error("Layer 2 should be preserved in the split output")
	}

	// --- Verify continuation indicators appear in pairs ---
	hasContinuedOn := strings.Contains(allContent, "continued on next comment")
	hasContinuedFrom := strings.Contains(allContent, "continued from previous comment")
	if hasContinuedOn != hasContinuedFrom {
		t.Error("continuation indicators should appear in pairs: 'continued on next' and 'continued from previous'")
	}

	// --- Verify sentinel ordering ---
	// First comment should have the primary sentinel
	if !strings.Contains(firstRender[0], DashboardSentinel) {
		t.Error("first comment should contain primary dashboard sentinel")
	}
	// Subsequent comments should have continued sentinels
	for i := 1; i < len(firstRender); i++ {
		expectedSentinel := fmt.Sprintf(DashboardContinuedSentinelFmt, i+1)
		if !strings.Contains(firstRender[i], expectedSentinel) {
			t.Errorf("comment %d should contain continued sentinel %d", i+1, i+1)
		}
	}
}

func TestNewDashboardUpdater(t *testing.T) {
	client := newFakeVCSClient()
	updater := NewDashboardUpdater(client, "")

	if updater.VCSClient != client {
		t.Error("expected VCSClient to be set")
	}
	if updater.Renderer == nil {
		t.Error("expected Renderer to be non-nil")
	}
}
