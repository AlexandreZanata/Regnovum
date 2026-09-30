// The object store half of the backup tool: the smallest S3 client that can
// support a PostgreSQL PITR pipeline (P19-T04).
//
// Why it is written here rather than taken from a library: the repository's
// dependency policy approves a short, named list of packages, and none of them
// speaks S3. What a backup needs is four operations — put, get, head, list —
// plus delete for retention, over a path-style endpoint, signed with SigV4.
// That is a few hundred lines against the standard library, and it is the part
// of the dependency that has to be *auditable*: the credential this process
// carries can only write and read one prefix, and the code that signs a request
// is the place where that claim is true or false.
//
// Path-style addressing is used deliberately rather than virtual-host style:
// path-style works against every S3-compatible store (MinIO, Ceph, R2, the
// provider's own endpoint) with one endpoint string, while virtual-host style
// requires the endpoint to accept arbitrary subdomains and a certificate per
// bucket.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// ErrAbsent reports an object the store says does not exist. It is a value and
// not a string so that "the backup is not there" can be handled without reading
// an error message.
var ErrAbsent = errors.New("object does not exist in the store")

// ErrPresent reports an object that exists when the caller asked not to replace
// one.
var ErrPresent = errors.New("object already exists in the store")

// Object is one entry of a listing.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// StoreConfig is the whole configuration of an object store client. The
// credential is deliberately a field of a struct that is built from the
// environment and never from a command line: an access key in argv is visible
// to every process on the host for as long as the command runs.
type StoreConfig struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	Prefix    string
	Client    *http.Client
}

// StoreConfigFromEnvironment reads the store configuration, naming every
// variable that is missing rather than failing on the first one: a deployment
// that has to be fixed twice for the same reason is a deployment that will be
// fixed twice.
func StoreConfigFromEnvironment() (StoreConfig, error) {
	config := StoreConfig{
		Endpoint:  strings.TrimSuffix(os.Getenv("BACKUP_S3_ENDPOINT"), "/"),
		Bucket:    os.Getenv("BACKUP_S3_BUCKET"),
		Region:    os.Getenv("BACKUP_S3_REGION"),
		AccessKey: os.Getenv("BACKUP_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("BACKUP_S3_SECRET_KEY"),
		Prefix:    strings.Trim(os.Getenv("BACKUP_S3_PREFIX"), "/"),
	}
	if config.Region == "" {
		config.Region = "us-east-1"
	}
	missing := []string{}
	for name, value := range map[string]string{
		"BACKUP_S3_ENDPOINT":   config.Endpoint,
		"BACKUP_S3_BUCKET":     config.Bucket,
		"BACKUP_S3_ACCESS_KEY": config.AccessKey,
		"BACKUP_S3_SECRET_KEY": config.SecretKey,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return StoreConfig{}, fmt.Errorf("the object store is not configured: %s", strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(config.Endpoint, "http://") && !strings.HasPrefix(config.Endpoint, "https://") {
		return StoreConfig{}, fmt.Errorf("BACKUP_S3_ENDPOINT must name its scheme (http:// or https://), got %q", config.Endpoint)
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 5 * time.Minute}
	}
	return config, nil
}

// Store is a client for one bucket at one endpoint.
type Store struct {
	config StoreConfig
	now    func() time.Time
}

// NewStore builds a client. The clock is injectable so that a signature can be
// asserted in a test without freezing the process.
func NewStore(config StoreConfig) *Store {
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Store{config: config, now: func() time.Time { return time.Now().UTC() }}
}

// resolve turns an object name into the key this store uses, applying the
// configured prefix. Every caller goes through it, so a prefix cannot be
// forgotten in one place and applied in another.
func (s *Store) resolve(name string) string {
	return joinKey(s.config.Prefix, name)
}

// joinKey joins a prefix and a name with exactly one separator, and treats an
// empty prefix as no prefix rather than as a leading slash: a credential that
// owns the whole bucket is a legitimate (if broad) configuration, and the
// alternative is a key that only this client would ever produce.
func joinKey(prefix, name string) string {
	switch {
	case prefix == "":
		return name
	case name == "":
		return prefix
	default:
		return prefix + "/" + name
	}
}

// Put uploads a local file under a name, with the payload hash computed from
// the file. It refuses to replace an existing object when that was asked for:
// a WAL segment re-archived after a crash must land on the same bytes, and a
// base backup must never be silently overwritten.
func (s *Store) Put(name string, body *os.File, size int64, checksum [32]byte, ifAbsent bool) error {
	if ifAbsent {
		_, exists, err := s.headKey(s.resolve(name))
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: %s", ErrPresent, name)
		}
	}
	request, err := http.NewRequest(http.MethodPut, s.objectURL(name), body)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	request.ContentLength = size
	request.Header.Set("Content-Type", "application/octet-stream")
	if err := s.sign(request, bodyHash(checksum)); err != nil {
		return err
	}
	response, err := s.config.Client.Do(request)
	if err != nil {
		return fmt.Errorf("upload %s: %w", name, err)
	}
	defer response.Body.Close()
	if err := expectSuccess(response, "upload "+name); err != nil {
		return err
	}
	// The body is read to the end so that a connection that failed halfway is
	// reported here rather than by the next request on a reused socket.
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return fmt.Errorf("upload %s: %w", name, err)
	}
	return nil
}

// Get streams an object into a writer. The caller decides what to do with the
// bytes; nothing here buffers an object, because a base backup does not fit in
// memory.
func (s *Store) Get(name string, destination io.Writer) error {
	return s.getKey(s.resolve(name), destination)
}

func (s *Store) getKey(key string, destination io.Writer) error {
	request, err := http.NewRequest(http.MethodGet, s.urlForKey(key), nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	if err := s.sign(request, emptyPayload()); err != nil {
		return err
	}
	response, err := s.config.Client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", key, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrAbsent, key)
	}
	if err := expectSuccess(response, "download "+key); err != nil {
		return err
	}
	if _, err := io.Copy(destination, response.Body); err != nil {
		return fmt.Errorf("download %s: %w", key, err)
	}
	return nil
}

// Head reports whether an object exists and how large it is.
func (s *Store) Head(name string) (int64, bool, error) {
	return s.headKey(s.resolve(name))
}

func (s *Store) headKey(key string) (int64, bool, error) {
	request, err := http.NewRequest(http.MethodHead, s.urlForKey(key), nil)
	if err != nil {
		return 0, false, fmt.Errorf("build head request: %w", err)
	}
	if err := s.sign(request, emptyPayload()); err != nil {
		return 0, false, err
	}
	response, err := s.config.Client.Do(request)
	if err != nil {
		return 0, false, fmt.Errorf("head %s: %w", key, err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return 0, false, fmt.Errorf("head %s: %w", key, err)
	}
	switch response.StatusCode {
	case http.StatusOK:
		length := response.ContentLength
		if length < 0 {
			return 0, true, nil
		}
		return length, true, nil
	case http.StatusNotFound:
		return 0, false, nil
	default:
		return 0, false, describeFailure(response, "head "+key)
	}
}

// Delete removes one object. It is the only destructive operation of this
// client, and the caller that reaches it is the retention policy, which decides
// what is old before it decides what to delete.
func (s *Store) Delete(name string) error {
	request, err := http.NewRequest(http.MethodDelete, s.objectURL(name), nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	if err := s.sign(request, emptyPayload()); err != nil {
		return err
	}
	response, err := s.config.Client.Do(request)
	if err != nil {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	switch response.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		return describeFailure(response, "delete "+name)
	}
}

// List returns every object under a name prefix, ordered by key, following the
// continuation tokens of the listing API: a paginated result that stopped at
// the first page would be a retention policy that silently keeps everything.
func (s *Store) List(prefix string) ([]Object, error) {
	objects := []Object{}
	token := ""
	for {
		query := url.Values{}
		query.Set("list-type", "2")
		query.Set("prefix", s.resolve(prefix))
		if token != "" {
			query.Set("continuation-token", token)
		}
		request, err := http.NewRequest(http.MethodGet, s.bucketURL()+"?"+query.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("build list request: %w", err)
		}
		if err := s.sign(request, emptyPayload()); err != nil {
			return nil, err
		}
		response, err := s.config.Client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		if response.StatusCode != http.StatusOK {
			return nil, describeFailureBody(response, body, "list "+prefix)
		}
		page := struct {
			Contents []struct {
				Key          string `xml:"Key"`
				Size         int64  `xml:"Size"`
				LastModified string `xml:"LastModified"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}{}
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("list %s: the store answered with a document this client cannot read: %w", prefix, err)
		}
		for _, entry := range page.Contents {
			modified, err := time.Parse(time.RFC3339, entry.LastModified)
			if err != nil {
				// A store that omits or formats Last-Modified differently is
				// not a reason to lose the entry: the key and size are what
				// the retention decision needs, and the zero time is visibly
				// not a modification time.
				modified = time.Time{}
			}
			objects = append(objects, Object{
				Key:          entry.Key,
				Size:         entry.Size,
				LastModified: modified.UTC(),
			})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
			return objects, nil
		}
		token = page.NextContinuationToken
	}
}

func (s *Store) bucketURL() string {
	return s.config.Endpoint + "/" + s.config.Bucket
}

// objectURL builds the address of an object *name*, resolving the prefix as it
// goes. It takes a name rather than a key so that a caller cannot reach the
// store outside the configured prefix by forgetting one step: the two-step
// version of this function was a way to address objects the credential is not
// supposed to own.
func (s *Store) objectURL(name string) string {
	return s.urlForKey(s.resolve(name))
}

func (s *Store) urlForKey(key string) string {
	return s.bucketURL() + "/" + encodePath(key)
}

// sign adds the SigV4 headers of a request whose payload hash is known. The
// signed header set is deliberately small — host, the payload hash and the date
// — because every header added to it is a header that must be reproduced
// exactly on both sides for the signature to verify.
func (s *Store) sign(request *http.Request, payloadHash string) error {
	parsed, err := url.Parse(request.URL.String())
	if err != nil {
		return fmt.Errorf("sign request: %w", err)
	}
	amzDate := s.now().UTC().Format("20060102T150405Z")
	date := amzDate[:8]
	request.Header.Set("x-amz-date", amzDate)
	request.Header.Set("x-amz-content-sha256", payloadHash)

	// The host signed is the one the request will carry, which is the URL's:
	// signing a host the wire does not send is a signature that never matches.
	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", parsed.Host, payloadHash, amzDate)

	canonicalRequest := strings.Join([]string{
		request.Method,
		canonicalURI(parsed),
		canonicalQuery(parsed),
		canonicalHeaders,
		strings.Join(signedHeaders, ";"),
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{date, s.config.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(sha256Sum([]byte(canonicalRequest))),
	}, "\n")

	signature := hex.EncodeToString(hmacSum(signingKey(s.config.SecretKey, date, s.config.Region), []byte(stringToSign)))
	request.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.config.AccessKey, scope, strings.Join(signedHeaders, ";"), signature,
	))
	return nil
}

// signingKey derives the request's signing key. It is the "derived key" of the
// SigV4 specification: four chained HMACs, none of which can be shortened
// without producing requests the store rejects.
func signingKey(secret, date, region string) []byte {
	key := hmacSum([]byte("AWS4"+secret), []byte(date))
	key = hmacSum(key, []byte(region))
	key = hmacSum(key, []byte("s3"))
	return hmacSum(key, []byte("aws4_request"))
}

func hmacSum(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

func sha256Sum(message []byte) []byte {
	sum := sha256.Sum256(message)
	return sum[:]
}

func emptyPayload() string {
	return hex.EncodeToString(sha256Sum(nil))
}

func bodyHash(sum [32]byte) string {
	return hex.EncodeToString(sum[:])
}

// canonicalURI encodes the path the way the signature expects it: every segment
// percent-encoded, with the slashes kept. A key that needed different encoding
// in the signature and on the wire would fail to authenticate for a reason that
// looks like a credential problem.
func canonicalURI(parsed *url.URL) string {
	if parsed.EscapedPath() == "" {
		return "/"
	}
	return parsed.EscapedPath()
}

// canonicalQuery sorts and encodes the query parameters, which is what makes
// the same request sign the same way twice.
func canonicalQuery(parsed *url.URL) string {
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return parsed.RawQuery
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		sorted := append([]string(nil), values[key]...)
		sort.Strings(sorted)
		for _, value := range sorted {
			parts = append(parts, encodeKey(key)+"="+encodeKey(value))
		}
	}
	return strings.Join(parts, "&")
}

// encodePath percent-encodes every segment of a key, keeping the separators.
func encodePath(key string) string {
	segments := strings.Split(key, "/")
	for index, segment := range segments {
		segments[index] = encodeKey(segment)
	}
	return strings.Join(segments, "/")
}

// encodeKey is the RFC 3986 encoding SigV4 requires: unreserved characters
// stand, everything else is uppercase percent-encoded, and a space is %20
// rather than a plus.
func encodeKey(text string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var builder strings.Builder
	for _, character := range []byte(text) {
		if strings.IndexByte(unreserved, character) >= 0 {
			builder.WriteByte(character)
			continue
		}
		fmt.Fprintf(&builder, "%%%02X", character)
	}
	return builder.String()
}

func expectSuccess(response *http.Response, what string) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	return describeFailure(response, what)
}

// describeFailure reads the store's error document and reports the code it
// named, because a bare status code sends an operator to a search engine while
// "AccessDenied" sends them to the credential's policy.
func describeFailure(response *http.Response, what string) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return fmt.Errorf("%s: %s (and its error body could not be read: %w)", what, response.Status, err)
	}
	return describeFailureBody(response, body, what)
}

func describeFailureBody(response *http.Response, body []byte, what string) error {
	document := struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}{}
	if err := xml.Unmarshal(body, &document); err == nil && document.Code != "" {
		if document.Message != "" {
			return fmt.Errorf("%s: %s: %s: %s", what, response.Status, document.Code, document.Message)
		}
		return fmt.Errorf("%s: %s: %s", what, response.Status, document.Code)
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200]
	}
	if text == "" {
		return fmt.Errorf("%s: %s", what, response.Status)
	}
	return fmt.Errorf("%s: %s: %s", what, response.Status, text)
}
