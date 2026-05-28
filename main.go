package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ulikunitz/xz"

	"updateweb/pkg/version"
)

const maxUploadSize = 100 * 1024 * 1024 // 100MB

//go:embed templates/*
var templatesFS embed.FS

var targetDir string

func main() {
	portFlag := flag.String("port", "8080", "Web server port")
	pathFlag := flag.String("path", "", "Target directory to deploy extracted files (required)")
	versionFlag := flag.Bool("version", false, "Print version information and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.Detailed())
		os.Exit(0)
	}

	if strings.TrimSpace(*pathFlag) == "" {
		fmt.Fprintln(os.Stderr, "Error: -path argument is required")
		flag.Usage()
		os.Exit(1)
	}

	absPath, err := filepath.Abs(*pathFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving path: %v\n", err)
		os.Exit(1)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: target directory %q does not exist\n", absPath)
		} else {
			fmt.Fprintf(os.Stderr, "Error checking target directory: %v\n", err)
		}
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: %q is not a directory\n", absPath)
		os.Exit(1)
	}

	targetDir = absPath
	log.Printf("updateweb version %s starting...", version.Info())
	log.Printf("Target directory: %s", targetDir)
	log.Printf("Listening on :%s", *portFlag)

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.MaxMultipartMemory = 8 << 20 // 8MB memory; spill to disk after
	tmpl := template.Must(template.ParseFS(templatesFS, "templates/*"))
	r.SetHTMLTemplate(tmpl)

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{
			"TargetDir": targetDir,
		})
	})

	r.POST("/upload", handleUpload)

	if err := r.Run(":" + *portFlag); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

type uploadResponse struct {
	OK       bool     `json:"ok"`
	Message  string   `json:"message"`
	Files    []string `json:"files,omitempty"`
	Duration string   `json:"duration,omitempty"`
}

func handleUpload(c *gin.Context) {
	start := time.Now()

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadSize)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, uploadResponse{Message: "missing file: " + err.Error()})
		return
	}

	if fileHeader.Size > maxUploadSize {
		c.JSON(http.StatusBadRequest, uploadResponse{Message: "file too large (max 100MB)"})
		return
	}

	kind := detectArchiveKind(fileHeader.Filename)
	if kind == "" {
		c.JSON(http.StatusBadRequest, uploadResponse{Message: "unsupported format; allowed: .zip, .tar.gz, .tgz, .tar.xz"})
		return
	}

	tmpDir, err := os.MkdirTemp("/tmp", "upload-*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, uploadResponse{Message: "failed to create temp dir: " + err.Error()})
		return
	}
	defer os.RemoveAll(tmpDir)

	tmpFile := filepath.Join(tmpDir, filepath.Base(fileHeader.Filename))
	if err := c.SaveUploadedFile(fileHeader, tmpFile); err != nil {
		c.JSON(http.StatusInternalServerError, uploadResponse{Message: "failed to save upload: " + err.Error()})
		return
	}

	stageDir := filepath.Join(tmpDir, "stage")
	if err := os.Mkdir(stageDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, uploadResponse{Message: "failed to prepare stage: " + err.Error()})
		return
	}

	files, err := extractArchive(tmpFile, stageDir, kind)
	if err != nil {
		c.JSON(http.StatusBadRequest, uploadResponse{Message: "archive verification failed: " + err.Error()})
		return
	}
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, uploadResponse{Message: "archive contains no files"})
		return
	}

	if err := clearDir(targetDir); err != nil {
		c.JSON(http.StatusInternalServerError, uploadResponse{Message: "failed to clean target dir: " + err.Error()})
		return
	}

	if err := moveTree(stageDir, targetDir); err != nil {
		c.JSON(http.StatusInternalServerError, uploadResponse{Message: "failed to deploy files: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, uploadResponse{
		OK:       true,
		Message:  fmt.Sprintf("Deployed %d file(s) to %s", len(files), targetDir),
		Files:    files,
		Duration: time.Since(start).Truncate(time.Millisecond).String(),
	})
}

func detectArchiveKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".zip"):
		return "zip"
	case strings.HasSuffix(n, ".tar.gz"), strings.HasSuffix(n, ".tgz"):
		return "tgz"
	case strings.HasSuffix(n, ".tar.xz"):
		return "txz"
	}
	return ""
}

func extractArchive(srcFile, destDir, kind string) ([]string, error) {
	switch kind {
	case "zip":
		return extractZip(srcFile, destDir)
	case "tgz":
		return extractTarStream(srcFile, destDir, "gzip")
	case "txz":
		return extractTarStream(srcFile, destDir, "xz")
	}
	return nil, errors.New("unknown archive kind")
}

func extractZip(srcFile, destDir string) ([]string, error) {
	zr, err := zip.OpenReader(srcFile)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	var written []string
	for _, zf := range zr.File {
		target, err := safeJoin(destDir, zf.Name)
		if err != nil {
			return nil, err
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("open entry %s: %w", zf.Name, err)
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, zf.Mode()|0o200)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("create %s: %w", target, err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return nil, fmt.Errorf("write %s: %w", target, err)
		}
		rc.Close()
		out.Close()
		written = append(written, zf.Name)
	}
	return written, nil
}

func extractTarStream(srcFile, destDir, compression string) ([]string, error) {
	f, err := os.Open(srcFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var r io.Reader
	switch compression {
	case "gzip":
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		r = gz
	case "xz":
		xr, err := xz.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("xz reader: %w", err)
		}
		r = xr
	default:
		return nil, fmt.Errorf("unknown compression %q", compression)
	}

	tr := tar.NewReader(r)
	var written []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return nil, err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777|0o700); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o200)
			if err != nil {
				return nil, fmt.Errorf("create %s: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return nil, fmt.Errorf("write %s: %w", target, err)
			}
			out.Close()
			written = append(written, hdr.Name)
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return nil, fmt.Errorf("symlink %s: %w", target, err)
			}
			written = append(written, hdr.Name)
		default:
			// skip other types (block/char/fifo)
		}
	}
	return written, nil
}

func safeJoin(base, name string) (string, error) {
	cleaned := filepath.Clean("/" + name)
	target := filepath.Join(base, cleaned)
	rel, err := filepath.Rel(base, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("unsafe path in archive: %s", name)
	}
	return target, nil
}

func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func moveTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if err := os.Rename(from, to); err != nil {
			// fallback to copy when rename fails (cross-device)
			if err := copyPath(from, to); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
