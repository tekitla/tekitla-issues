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

// gitRun corre git en dir, sin modificar nada, y devuelve la salida cruda.
// Las rutas salen sin comillas ni escapes (core.quotepath=off).
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

// folderLog devuelve, para cada archivo actual de files (nombres dentro de
// dir), los commits que lo tocan, del más viejo al más nuevo, con la ruta
// que tenía en cada uno (relativa a la raíz del repo). Un solo git log de
// toda la carpeta, con detección de renombres (-M, la misma de --follow):
// se recorre del commit más nuevo al más viejo y un renombre pasa la
// identidad del archivo a su nombre anterior.
//
// Solo la historia de main (--first-parent): un cambio hecho en una rama
// cuenta cuando llega a main, en el merge, con su fecha. Así la historia es
// una línea y dos ramas paralelas no se intercalan (eso inventaría
// transiciones de ida y vuelta).
func folderLog(dir string, files []string) (map[string][]Version, error) {
	prefix, err := gitRun(dir, "rev-parse", "--show-prefix")
	if err != nil {
		return nil, fmt.Errorf("-historico lee git y %s no es un repo git: %w", dir, err)
	}
	// alias: ruta (en el commit que se está leyendo) -> archivo actual.
	alias := map[string]string{}
	for _, f := range files {
		alias[strings.TrimSpace(string(prefix))+f] = f
	}
	out, err := gitRun(dir, "log", "--first-parent", "--diff-merges=first-parent", "-M", "--name-status", "--format="+commitMark+"%H %cI", "--", ".")
	if err != nil {
		return nil, err
	}
	versions := map[string][]Version{}
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
		for _, l := range lines[1:] {
			f := strings.Split(strings.TrimRight(l, "\r"), "\t")
			if len(f) < 2 || f[0] == "" {
				continue
			}
			p := f[len(f)-1]
			file, ok := alias[p]
			switch f[0][0] {
			case 'D':
				// Antes de borrarse, esa ruta era otro archivo.
				delete(alias, p)
				continue
			case 'R':
				if len(f) != 3 {
					return nil, fmt.Errorf("git log: renombre inesperado %q", l)
				}
				delete(alias, p)
				if ok {
					alias[f[1]] = file
				}
			case 'A':
				delete(alias, p)
			}
			if ok {
				versions[file] = append(versions[file], Version{Commit: head[0], Date: date, Path: p})
			}
		}
	}
	for _, vs := range versions {
		for i, j := 0, len(vs)-1; i < j; i, j = i+1, j-1 {
			vs[i], vs[j] = vs[j], vs[i]
		}
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
	if strings.HasSuffix(header, " missing\n") {
		return nil, fmt.Errorf("no está en el commit")
	}
	f := strings.Fields(header)
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

// readStates completa la línea Estado de cada versión de doc, leída con
// ParseDoc como en la importación. Del contenido solo se guarda el Estado.
func readStates(blobs *blobReader, doc *Doc, versions []Version) []Version {
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
	return versions
}
