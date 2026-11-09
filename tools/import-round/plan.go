package main

import (
	"sort"
)

// Existing es lo que ya hay en Tekitla: número -> clave de origen de los
// issues importados (los creados en Tekitla tienen clave vacía).
type Existing map[int32]string

// Duplicate es un número repetido en Round: quién se quedó con RND-<n> y
// quién quedó afuera.
type Duplicate struct {
	Number int32
	// Winner nil: el número ya es de un issue de Tekitla sin archivo (o
	// creado a mano), y ningún archivo se lo queda.
	Winner *Doc
	// HolderKey es la clave del issue que ya tiene el número cuando Winner
	// es nil ("" si se creó en Tekitla).
	HolderKey string
	Losers    []*Doc
}

// Plan es qué se importa y qué no, decidido sin depender del orden en que se
// leyeron los archivos ni de git (§4 del issue 025).
type Plan struct {
	// Import son los archivos que se mandan a ImportIssue, por número.
	Import     []*Doc
	Duplicates []Duplicate
	// Orphans son las claves importadas antes que ya no tienen archivo (por
	// número). No se borran.
	Orphans []Orphan
}

// Orphan es un issue importado cuyo archivo ya no está.
type Orphan struct {
	Number    int32
	SourceKey string
}

// MakePlan decide qué archivo se queda con cada número:
//  1. el que ya está importado con ese número;
//  2. si no, el primero por slug (comparación de bytes);
//  3. si el número ya es de un issue sin archivo (o creado en Tekitla),
//     ninguno: todos quedan afuera y en el reporte.
func MakePlan(docs []*Doc, existing Existing) Plan {
	byNumber := map[int32][]*Doc{}
	keys := map[string]bool{}
	for _, d := range docs {
		byNumber[d.Number] = append(byNumber[d.Number], d)
		keys[d.SourceKey] = true
	}
	numbers := make([]int32, 0, len(byNumber))
	for n := range byNumber {
		numbers = append(numbers, n)
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })

	var plan Plan
	for _, n := range numbers {
		cands := append([]*Doc(nil), byNumber[n]...)
		sort.Slice(cands, func(i, j int) bool { return cands[i].Slug < cands[j].Slug })
		holder, taken := existing[n]

		var winner *Doc
		switch {
		case taken:
			for _, c := range cands {
				if holder != "" && c.SourceKey == holder {
					winner = c
				}
			}
		default:
			winner = cands[0]
		}
		var losers []*Doc
		for _, c := range cands {
			if c != winner {
				losers = append(losers, c)
			}
		}
		if winner != nil {
			plan.Import = append(plan.Import, winner)
		}
		if len(losers) > 0 {
			d := Duplicate{Number: n, Winner: winner, Losers: losers}
			if winner == nil {
				d.HolderKey = holder
			}
			plan.Duplicates = append(plan.Duplicates, d)
		}
	}

	for n, key := range existing {
		if key != "" && !keys[key] {
			plan.Orphans = append(plan.Orphans, Orphan{Number: n, SourceKey: key})
		}
	}
	sort.Slice(plan.Orphans, func(i, j int) bool { return plan.Orphans[i].Number < plan.Orphans[j].Number })
	return plan
}
