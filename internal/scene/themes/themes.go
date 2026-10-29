// Package themes registers every theme: importing it is what makes them drawable and switchable. A theme
// package that registers nothing yet is imported too, so building one never edits this file.
package themes

import (
	_ "github.com/project-kikkuli/yamnaya/internal/scene/anthill"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/carrier"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/civilizations"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/forest"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/islands"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/kaiju"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/moai"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/mountains"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/pyramids"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/skyline"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/stocks"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/subway"
)
