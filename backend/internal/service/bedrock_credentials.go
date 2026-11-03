package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"golang.org/x/sys/unix"
)

const bedrockCredentialsFileEnv = "SUB2API_BEDROCK_CREDENTIALS_FILE"
const bedrockCredentialSkew = 5 * time.Minute
const bedrockCredentialMaxSize = 16 * 1024

var errBedrockCredentials = errors.New("bedrock credential file unavailable or invalid")
var bedrockRegionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)

// Only the operator environment selects the file. Never consult the SDK default
// chain: this process may share a host with unrelated, privileged AWS credentials.
var bedrockFileCache struct {
	sync.Mutex
	path  string
	cache *aws.CredentialsCache
}

func operatorBedrockCredentials() (aws.CredentialsProvider, error) {
	path := os.Getenv(bedrockCredentialsFileEnv)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errBedrockCredentials
	}
	bedrockFileCache.Lock()
	defer bedrockFileCache.Unlock()
	// One registry entry bounds memory even if the operator changes configuration.
	// Existing signers retain their provider; account data never controls this key.
	if bedrockFileCache.cache == nil || bedrockFileCache.path != path {
		bedrockFileCache.path = path
		bedrockFileCache.cache = aws.NewCredentialsCache(&bedrockFileCredentials{path: path}, func(o *aws.CredentialsCacheOptions) {
			o.ExpiryWindow = bedrockCredentialSkew
		})
	}
	return bedrockFileCache.cache, nil
}

type bedrockFileCredentials struct{ path string }

// openBedrockCredentialFile walks with directory FDs, refusing symlinks in any
// component and avoiding a check/open race. Files must be root-owned 0600/0640
// (or stricter). 0640 allows a dedicated application group on read-only mounts.
// Parent directories must be root-owned and not writable by other users; sticky
// root-owned directories are safe because only root may replace root-owned entries.
func openBedrockCredentialFile(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errBedrockCredentials
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errBedrockCredentials
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, errBedrockCredentials
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != 0 {
			_ = unix.Close(fd)
			return nil, errBedrockCredentials
		}
		if i < len(parts)-1 {
			if st.Mode&0022 != 0 && st.Mode&unix.S_ISVTX == 0 {
				_ = unix.Close(fd)
				return nil, errBedrockCredentials
			}
		} else if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777&^uint32(0640) != 0 || st.Size <= 0 || st.Size > bedrockCredentialMaxSize {
			_ = unix.Close(fd)
			return nil, errBedrockCredentials
		}
	}
	return os.NewFile(uintptr(fd), "bedrock-credentials"), nil
}

func (p *bedrockFileCredentials) Retrieve(ctx context.Context) (aws.Credentials, error) {
	if err := ctx.Err(); err != nil {
		return aws.Credentials{}, err
	}
	file, err := openBedrockCredentialFile(p.path)
	if err != nil {
		return aws.Credentials{}, errBedrockCredentials
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, bedrockCredentialMaxSize+1))
	if err != nil || len(data) > bedrockCredentialMaxSize {
		return aws.Credentials{}, errBedrockCredentials
	}
	if err := ctx.Err(); err != nil {
		return aws.Credentials{}, err
	}
	fields, err := decodeBedrockCredentialFile(data)
	if err != nil {
		return aws.Credentials{}, errBedrockCredentials
	}
	expiry, err := time.Parse(time.RFC3339Nano, fields["Expiration"])
	if err != nil || !expiry.After(time.Now().Add(bedrockCredentialSkew)) {
		return aws.Credentials{}, errBedrockCredentials
	}
	return aws.Credentials{
		AccessKeyID: fields["AccessKeyId"], SecretAccessKey: fields["SecretAccessKey"], SessionToken: fields["SessionToken"],
		CanExpire: true, Expires: expiry, Source: "BedrockOperatorCredentialFile",
	}, nil
}

// Exact names, string values, no duplicates/unknown fields/trailing JSON. A
// session token and explicit expiry are mandatory: static host keys are not a
// substitute for the short-lived application-role credential contract.
func decodeBedrockCredentialFile(data []byte) (map[string]string, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errBedrockCredentials
	}
	fields := make(map[string]string, 4)
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return nil, errBedrockCredentials
		}
		name, ok := token.(string)
		if !ok {
			return nil, errBedrockCredentials
		}
		switch name {
		case "AccessKeyId", "SecretAccessKey", "SessionToken", "Expiration":
		default:
			return nil, errBedrockCredentials
		}
		if _, exists := fields[name]; exists {
			return nil, errBedrockCredentials
		}
		var value string
		if dec.Decode(&value) != nil || value == "" || strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return nil, errBedrockCredentials
		}
		fields[name] = value
	}
	if _, err = dec.Token(); err != nil {
		return nil, errBedrockCredentials
	}
	if _, err = dec.Token(); err != io.EOF || len(fields) != 4 {
		return nil, errBedrockCredentials
	}
	return fields, nil
}
