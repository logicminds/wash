package plugin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type mockExternalPluginScript struct {
	mock.Mock
	path string
}

func (m *mockExternalPluginScript) Path() string {
	return m.path
}

func (m *mockExternalPluginScript) InvokeAndWait(ctx context.Context, args ...string) ([]byte, error) {
	retValues := m.Called(ctx, args)
	return retValues.Get(0).([]byte), retValues.Error(1)
}

// We make ctx an interface{} so that this method could
// be used when the caller generates a context using e.g.
// context.Background()
func (m *mockExternalPluginScript) OnInvokeAndWait(ctx interface{}, args ...string) *mock.Call {
	return m.On("InvokeAndWait", ctx, args)
}

type ExternalPluginEntryTestSuite struct {
	suite.Suite
}

func (suite *ExternalPluginEntryTestSuite) EqualTimeAttr(expected time.Time, actual time.Time, msgAndArgs ...interface{}) {
	suite.WithinDuration(expected, actual, 1*time.Second, msgAndArgs...)
}

func (suite *ExternalPluginEntryTestSuite) TestUnixSecondsToTimeAttr() {
	suite.EqualTimeAttr(time.Time{}, unixSecondsToTimeAttr(0))

	t := time.Now()
	suite.EqualTimeAttr(t, unixSecondsToTimeAttr(t.Unix()))
}

func (suite *ExternalPluginEntryTestSuite) TestDecodeAttributes() {
	atime := time.Now()
	mtime := time.Now()
	ctime := time.Now()

	decodedAttributes := decodedAttributes{
		Atime: atime.Unix(),
		Mtime: mtime.Unix(),
		Ctime: ctime.Unix(),
		Size:  10,
		Valid: 1 * time.Second,
	}

	// Test that the attributes are correctly decoded
	attributes, err := decodedAttributes.toAttributes()
	if suite.NoError(err) {
		suite.EqualTimeAttr(atime, attributes.Atime)
		suite.EqualTimeAttr(mtime, attributes.Mtime)
		suite.EqualTimeAttr(ctime, attributes.Ctime)
		suite.Equal(uint64(10), attributes.Size)
		suite.Equal(
			os.FileMode(0),
			attributes.Mode,
			"Expected the decoded attributes to have no mode field",
		)
	}

	// Test that the mode is correctly decoded
	decodedAttributes.Mode = "0xff"
	attributes, err = decodedAttributes.toAttributes()
	if suite.NoError(err) {
		suite.Equal(os.FileMode(255), attributes.Mode)
	}

	// Test that toAttributes() returns an error when the mode
	// cannot be decoded
	decodedAttributes.Mode = "not a number"
	attributes, err = decodedAttributes.toAttributes()
	suite.Error(err)
}

func (suite *ExternalPluginEntryTestSuite) TestDecodeCacheConfig() {
	decodedTTLs := decodedCacheTTLs{
		List:     10,
		Open:     15,
		Metadata: 20,
	}

	config := decodedTTLs.toCacheConfig()

	suite.Equal(decodedTTLs.List*time.Second, config.getTTLOf(List))
	suite.Equal(decodedTTLs.Open*time.Second, config.getTTLOf(Open))
	suite.Equal(decodedTTLs.Metadata*time.Second, config.getTTLOf(Metadata))
}

func (suite *ExternalPluginEntryTestSuite) TestDecodeExternalPluginEntry() {
	decodedEntry := decodedExternalPluginEntry{}

	_, err := decodedEntry.toExternalPluginEntry()
	suite.Regexp(regexp.MustCompile("name"), err)
	decodedEntry.Name = "decodedEntry"

	_, err = decodedEntry.toExternalPluginEntry()
	suite.Regexp(regexp.MustCompile("action"), err)
	decodedEntry.SupportedActions = []string{"list"}

	minimalEntry, err := decodedEntry.toExternalPluginEntry()
	if suite.NoError(err) {
		suite.Equal(decodedEntry.Name, minimalEntry.name)
		suite.Equal(decodedEntry.SupportedActions, minimalEntry.supportedActions)
	}

	decodedEntry.State = "some state"
	entryWithState, err := decodedEntry.toExternalPluginEntry()
	if suite.NoError(err) {
		suite.Equal(decodedEntry.State, entryWithState.state)
	}

	decodedEntry.CacheTTLs = decodedCacheTTLs{List: 1}
	entryWithCacheConfig, err := decodedEntry.toExternalPluginEntry()
	if suite.NoError(err) {
		expectedCacheConfig := newCacheConfig()
		expectedCacheConfig.SetTTLOf(List, decodedEntry.CacheTTLs.List*time.Second)
		suite.Equal(expectedCacheConfig, entryWithCacheConfig.CacheConfig())
	}

	decodedEntry.Attributes = decodedAttributes{Size: 10}
	entryWithAttributes, err := decodedEntry.toExternalPluginEntry()
	if suite.NoError(err) {
		suite.Equal(Attributes{Size: 10}, entryWithAttributes.attr)
	}

	decodedEntry.Attributes = decodedAttributes{Mode: "invalid mode"}
	_, err = decodedEntry.toExternalPluginEntry()
	suite.Error(err)
}

func (suite *ExternalPluginEntryTestSuite) TestName() {
	entry := ExternalPluginEntry{name: "foo"}
	suite.Equal("foo", entry.Name())
}

func (suite *ExternalPluginEntryTestSuite) TestCacheConfig() {
	entry := ExternalPluginEntry{cacheConfig: newCacheConfig()}
	suite.Equal(newCacheConfig(), entry.CacheConfig())
}

// TODO: There's a bit of duplication between TestList, TestOpen,
// and TestMetadata that could be refactored. Not worth doing right
// now since the refactor may make the tests harder to understand,
// but could be worth considering later if we add similarly structured
// actions.

func (suite *ExternalPluginEntryTestSuite) TestList() {
	mockScript := &mockExternalPluginScript{path: "plugin_script"}
	entry := ExternalPluginEntry{
		script:   mockScript,
		washPath: "/foo",
	}

	ctx := context.Background()
	mockInvokeAndWait := func(stdout []byte, err error) {
		mockScript.OnInvokeAndWait(ctx, "list", entry.washPath, entry.state).Return(stdout, err).Once()
	}

	// Test that if InvokeAndWait errors, then List returns its error
	mockErr := fmt.Errorf("execution error")
	mockInvokeAndWait([]byte{}, mockErr)
	_, err := entry.List(ctx)
	suite.EqualError(mockErr, err.Error())

	// Test that List returns an error if stdout does not have the right
	// output format
	mockInvokeAndWait([]byte("bad format"), nil)
	_, err = entry.List(ctx)
	suite.Regexp(regexp.MustCompile("stdout"), err)

	// Test that List properly decodes the entries from stdout
	stdout := "[" +
		"{\"name\":\"foo\",\"supported_actions\":[\"list\"]}" +
		"]"
	mockInvokeAndWait([]byte(stdout), nil)
	entries, err := entry.List(ctx)
	if suite.NoError(err) {
		expectedEntries := []Entry{
			&ExternalPluginEntry{
				name:             "foo",
				supportedActions: []string{"list"},
				cacheConfig:      newCacheConfig(),
				washPath:         entry.washPath + "/" + "foo",
				script:           entry.script,
			},
		}

		suite.Equal(expectedEntries, entries)
	}
}

func (suite *ExternalPluginEntryTestSuite) TestOpen() {
	mockScript := &mockExternalPluginScript{path: "plugin_script"}
	entry := ExternalPluginEntry{
		script:   mockScript,
		washPath: "/foo",
	}

	ctx := context.Background()
	mockInvokeAndWait := func(stdout []byte, err error) {
		mockScript.OnInvokeAndWait(ctx, "read", entry.washPath, entry.state).Return(stdout, err).Once()
	}

	// Test that if InvokeAndWait errors, then Open returns its error
	mockErr := fmt.Errorf("execution error")
	mockInvokeAndWait([]byte{}, mockErr)
	_, err := entry.Open(ctx)
	suite.EqualError(mockErr, err.Error())

	// Test that Open wraps all of stdout into a SizedReader
	stdout := "foo"
	mockInvokeAndWait([]byte(stdout), nil)
	rdr, err := entry.Open(ctx)
	if suite.NoError(err) {
		expectedRdr := bytes.NewReader([]byte(stdout))
		suite.Equal(expectedRdr, rdr)
	}
}

func (suite *ExternalPluginEntryTestSuite) TestMetadata() {
	mockScript := &mockExternalPluginScript{path: "plugin_script"}
	entry := ExternalPluginEntry{
		script:   mockScript,
		washPath: "/foo",
	}

	ctx := context.Background()
	mockInvokeAndWait := func(stdout []byte, err error) {
		mockScript.OnInvokeAndWait(ctx, "metadata", entry.washPath, entry.state).Return(stdout, err).Once()
	}

	// Test that if InvokeAndWait errors, then Metadata returns its error
	mockErr := fmt.Errorf("execution error")
	mockInvokeAndWait([]byte{}, mockErr)
	_, err := entry.Metadata(ctx)
	suite.EqualError(mockErr, err.Error())

	// Test that Metadata returns an error if stdout does not have the right
	// output format
	mockInvokeAndWait([]byte("bad format"), nil)
	_, err = entry.Metadata(ctx)
	suite.Regexp(regexp.MustCompile("stdout"), err)

	// Test that Metadata properly decodes the entries from stdout
	stdout := "{\"key\":\"value\"}"
	mockInvokeAndWait([]byte(stdout), nil)
	metadata, err := entry.Metadata(ctx)
	if suite.NoError(err) {
		expectedMetadata := MetadataMap{"key": "value"}
		suite.Equal(expectedMetadata, metadata)
	}
}

func (suite *ExternalPluginEntryTestSuite) TestAttr() {
	entry := ExternalPluginEntry{attr: Attributes{Size: 10}}
	suite.Equal(entry.attr, entry.Attr())
}

// TODO: Add tests for stdoutStreamer, Stream and Exec
// once the API for Stream and Exec's at a more stable
// state.

func TestExternalPluginEntry(t *testing.T) {
	suite.Run(t, new(ExternalPluginEntryTestSuite))
}