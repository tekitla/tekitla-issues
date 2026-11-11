package main

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Lectura de git para el histórico (issue 036). Solo lectura: git log y
// git cat-file no tocan la copia.

// gitRun corre git en dir y devuelve la salida cruda.
func gitRun(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.quotepath=off"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// commitMark separa los commits en la salida de git log.
const commitMark = "\x1ecommit "

// fileLog devuelve los commits que tocan file (en dir), del más viejo al
// más nuevo, siguiendo renombres, con la ruta que tenía el archivo en cada
// uno (relativa a la raíz del repo). Sin merges: el cambio cuenta en el
// commit que lo hizo.
func fileLog(dir, file string) ([]Version, error) {
	out, err := gitRun(dir, "log", "--follow", "--no-merges", "--format="+commitMark+"%H %cI", "--name-only", "--", file)
	if err != nil {
		return nil, err
	}
	var versions []Version
	for _, chunk := range strings.Split(string(out), commitMark)[1:] {
		lines := strings.Split(strings.TrimSpace(chunk), "\n")
		head := strings.Fields(lines[0])
		if len(head) != 2 {
			return nil, fmt.Errorf("git log: línea inesperada %q", lines[0])
		}
		date, err := time.Parse(time.RFC3339, head[1])
		if err != nil {
			return nil, fmt.Errorf("git log: fecha %q: %w", head[1], err)
		}
		v := Version{Commit: head[0], Date: date}
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" {
				v.Path = l
			}
		}
		if v.Path == "" {
			return nil, fmt.Errorf("git log: el commit %s no trae la ruta de %s", short(v.Commit), file)
		}
		versions = append(versions, v)
	}
	for i, j := 0, len(versions)-1; i < j; i, j = i+1, j-1 {
		versions[i], versions[j] = versions[j], versions[i]
	}
	return versions, nil
}

// blobReader lee archivos de commits con un solo git cat-file --batch.
type blobReader struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func newBlobReader(dir string) (*blobReader, error) {
	cmd := exec.Command("git", "-C", dir, "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	return &blobReader{cmd: cmd, in: in, out: bufio.NewReader(out)}, nil
}

// read devuelve el contenido de file (relativo a la raíz) en commit.
func (b *blobReader) read(commit, file string) ([]byte, error) {
	if strings.ContainsAny(file, "\n") {
		return nil, fmt.Errorf("ruta con salto de línea: %q", file)
	}
	if _, err := fmt.Fprintf(b.in, "%s:%s\n", commit, file); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	header, err := b.out.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	f := strings.Fields(header)
	if len(f) == 2 && f[1] == "missing" {
		return nil, fmt.Errorf("no está en el commit")
	}
	if len(f) != 3 || f[1] != "blob" {
		return nil, fmt.Errorf("git cat-file: respuesta inesperada %q", strings.TrimSpace(header))
	}
	size, err := strconv.Atoi(f[2])
	if err != nil {
		return nil, fmt.Errorf("git cat-file: tamaño %q", f[2])
	}
	buf := make([]byte, size+1) // + el salto de línea final
	if _, err := io.ReadFull(b.out, buf); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	return buf[:size], nil
}

func (b *blobReader) Close() error {
	b.in.Close()
	return b.cmd.Wait()
}

// readVersions arma las versiones de doc (archivo en dir): la línea Estado
// de cada commit, leída con ParseDoc como en la importación. Del contenido
// solo se guarda el Estado.
func readVersions(dir string, blobs *blobReader, doc *Doc) ([]Version, error) {
	versions, err := fileLog(dir, doc.File)
	if err != nil {
		return nil, err
	}
	for i := range versions {
		v := &versions[i]
		content, err := blobs.read(v.Commit, v.Path)
		if err != nil {
			v.Err = err.Error()
			continue
		}
		// Con el nombre actual: un nombre viejo puede no ser <número>-<slug>.md
		// y solo importa el Estado.
		old, err := ParseDoc(v.Path, doc.File, content)
		if err != nil {
			v.Err = err.Error()
			continue
		}
		v.RawState = old.RawState
	}
	return versions, nil
}
