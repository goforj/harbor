// App-owned Wire injector. EDIT THIS FILE.
// Add repository providers here, or use `forj make:model`.

package wire

import (
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/wire"
)

// repositorySet is a wire set for generated repositories.
var repositorySet = wire.NewSet(
	wire.Value(repositorySetPlaceholder{}),
	models.NewHarborStateRepo,
	models.NewOperationRepo,
	models.NewOperationTransitionRepo,
	models.NewProjectAppRepo,
	models.NewProjectRepo,
	models.NewProjectResourceRepo,
	models.NewProjectServiceRepo,
	models.NewRecentResourceRepo,
)

// repositorySetPlaceholder keeps repositorySet non-empty until repos are generated.
type repositorySetPlaceholder struct{}
