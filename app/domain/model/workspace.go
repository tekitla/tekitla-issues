package model

import "time"

// Workspace es el espacio de trabajo dueño de los proyectos.
//
// En la rebanada 1 cada usuario tiene solo su espacio personal (OwnerUID es su
// uid), que se crea en su primer acceso. Desde la rebanada 3 un espacio es un
// tenant de la plataforma (issue 005) y su ID es el id del tenant.
type Workspace struct {
	ID        string
	OwnerUID  string
	CreatedAt time.Time
}
