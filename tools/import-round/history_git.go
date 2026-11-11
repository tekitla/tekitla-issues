package main

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
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
	prefix, err := git(dir, "rev-parse", "--show-prefix")
	if err != nil {
		return nil, fmt.Errorf("-historico lee git y %s no es un repo git: %w", dir, err)
	}
	// Una copia superficial (clone --depth) daría un histórico falso: todo
	// nacería en el commit más viejo que tiene.
	if shallow, err := git(dir, "rev-parse", "--is-shallow-repository"); err != nil || shallow == "true" {
		return nil, fmt.Errorf("%s es una copia superficial (o no pude saberlo): -historico necesita toda la historia (git fetch --unshallow)", dir)
	}
	// prefix se calcula con la ruta del sistema, pero en git las rutas usan
	// siempre «/».
	prefix = filepath.ToSlash(prefix)
	// alias: ruta (en el commit que se está leyendo) -> archivo actual.
	alias := map[string]string{}
	for _, f := range files {
		alias[prefix+f] = f
	}
	// -m con --first-parent: el merge muestra su diff contra main (igual que
	// --diff-merges=first-parent, que pide git 2.31 o posterior).
	out, err := gitRun(dir, "log", "--first-parent", "-m", "-M", "--name-status", "--format="+commitMark+"%H %cI", "--", ".")
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
			for i := range f[1:] {
				f[i+1] = unquotePath(f[i+1])
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

// unquotePath: aun con core.quotepath=off, git pone entre comillas (con
// escapes de C) las rutas con «"», «\» o caracteres de control. Los escapes
// de C que usa git (\", \\, \t, \n, \ooo) son los mismos de Go.
func unquotePath(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// blobReader lee archivos de commits con un solo git cat-file --batch.
type blobReader struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *strings.Builder
	// broken: la lectura quedó desfasada; las siguientes fallan.
	broken error
}

func newBlobReader(dir string) (*blobReader, error) {
	cmd := exec.Command("git", "-C", dir, "cat-file", "--batch")
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
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
	return &blobReader{cmd: cmd, in: in, out: bufio.NewReader(out), stderr: stderr}, nil
}

// read devuelve el contenido de file (relativo a la raíz) en commit.
func (b *blobReader) read(commit, file string) ([]byte, error) {
	if b.broken != nil {
		return nil, b.broken
	}
	if strings.ContainsAny(file, "\n") {
		return nil, fmt.Errorf("ruta con salto de línea: %q", file)
	}
	if _, err := fmt.Fprintf(b.in, "%s:%s\n", commit, file); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	header, err := b.out.ReadString('\n')
	if err != nil {
		b.broken = fmt.Errorf("git cat-file: %w", err)
		return nil, b.broken
	}
	if strings.HasSuffix(header, " missing\n") {
		return nil, fmt.Errorf("no está en el commit")
	}
	// «<sha> <tipo> <tamaño>»: el sha no tiene espacios, así que los dos
	// últimos campos son el tipo y el tamaño.
	f := strings.Fields(header)
	if len(f) < 3 {
		b.broken = fmt.Errorf("git cat-file: respuesta inesperada %q", strings.TrimSpace(header))
		return nil, b.broken
	}
	size, err := strconv.Atoi(f[len(f)-1])
	if err != nil {
		b.broken = fmt.Errorf("git cat-file: tamaño %q", f[len(f)-1])
		return nil, b.broken
	}
	// Siempre se consume el contenido (+ el salto de línea final), aunque no
	// sea un blob: si no, la respuesta siguiente queda desfasada.
	buf := make([]byte, size+1)
	if _, err := io.ReadFull(b.out, buf); err != nil {
		b.broken = fmt.Errorf("git cat-file: %w", err)
		return nil, b.broken
	}
	if typ := f[len(f)-2]; typ != "blob" {
		return nil, fmt.Errorf("no es un archivo (%s)", typ)
	}
	return buf[:size], nil
}

// Close termina cat-file. Devuelve el error del proceso (con su stderr) o
// el primero que dejó la lectura desfasada: en los dos casos el histórico
// no es confiable.
func (b *blobReader) Close() error {
	b.in.Close()
	if err := b.cmd.Wait(); err != nil {
		return fmt.Errorf("git cat-file: %w: %s", err, strings.TrimSpace(b.stderr.String()))
	}
	return b.broken
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
