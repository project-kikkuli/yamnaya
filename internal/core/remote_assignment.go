package core

type RemoteAssignment struct {
	Origin           string
	Host             string
	Requester        string
	Goal             string
	Manager          string
	Parent           string
	ParentAssignment string
	Worker           string
	Assignment       Assignment
	Launch           *NewLaunch
	State            string
}
