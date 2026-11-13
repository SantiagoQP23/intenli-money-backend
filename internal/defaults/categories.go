// Package defaults holds the categories new workspaces start with and the
// icons a category may use. The app keeps the same icon list.
package defaults

// Category is one default category. Kind is INCOME or EXPENSE; Icon is a
// Lucide icon name.
type Category struct {
	Kind, Name, Icon string
}

// Personal is for PERSONAL and SHARED (family) workspaces.
var Personal = []Category{
	{"INCOME", "Salario", "briefcase"},
	{"INCOME", "Negocio propio", "store"},
	{"INCOME", "Inversiones", "trending-up"},
	{"INCOME", "Regalos", "gift"},
	{"INCOME", "Otros ingresos", "circle-plus"},

	{"EXPENSE", "Comida", "utensils"},
	{"EXPENSE", "Restaurantes", "coffee"},
	{"EXPENSE", "Transporte", "bus"},
	{"EXPENSE", "Vivienda", "house"},
	{"EXPENSE", "Servicios básicos", "zap"},
	{"EXPENSE", "Salud", "heart-pulse"},
	{"EXPENSE", "Educación", "graduation-cap"},
	{"EXPENSE", "Ropa", "shirt"},
	{"EXPENSE", "Hogar", "sofa"},
	{"EXPENSE", "Entretenimiento", "clapperboard"},
	{"EXPENSE", "Suscripciones", "repeat"},
	{"EXPENSE", "Cuidado personal", "sparkles"},
	{"EXPENSE", "Mascotas", "paw-print"},
	{"EXPENSE", "Regalos y donaciones", "heart-handshake"},
	{"EXPENSE", "Otros gastos", "circle-ellipsis"},
}

// Business is for BUSINESS workspaces.
var Business = []Category{
	{"INCOME", "Ventas", "shopping-cart"},
	{"INCOME", "Servicios prestados", "handshake"},
	{"INCOME", "Intereses", "percent"},
	{"INCOME", "Otros ingresos", "circle-plus"},

	{"EXPENSE", "Mercadería", "package"},
	{"EXPENSE", "Proveedores", "truck"},
	{"EXPENSE", "Sueldos", "users"},
	{"EXPENSE", "Arriendo", "building"},
	{"EXPENSE", "Servicios básicos", "zap"},
	{"EXPENSE", "Transporte y envíos", "send"},
	{"EXPENSE", "Marketing", "megaphone"},
	{"EXPENSE", "Impuestos", "landmark"},
	{"EXPENSE", "Comisiones bancarias", "credit-card"},
	{"EXPENSE", "Mantenimiento", "wrench"},
	{"EXPENSE", "Software y suscripciones", "laptop"},
	{"EXPENSE", "Honorarios profesionales", "scale"},
	{"EXPENSE", "Otros gastos", "circle-ellipsis"},
}

// PersonalEN is Personal in English, in the same order with the same icons.
var PersonalEN = []Category{
	{"INCOME", "Salary", "briefcase"},
	{"INCOME", "Own business", "store"},
	{"INCOME", "Investments", "trending-up"},
	{"INCOME", "Gifts received", "gift"},
	{"INCOME", "Other income", "circle-plus"},

	{"EXPENSE", "Groceries", "utensils"},
	{"EXPENSE", "Restaurants", "coffee"},
	{"EXPENSE", "Transportation", "bus"},
	{"EXPENSE", "Housing", "house"},
	{"EXPENSE", "Utilities", "zap"},
	{"EXPENSE", "Health", "heart-pulse"},
	{"EXPENSE", "Education", "graduation-cap"},
	{"EXPENSE", "Clothing", "shirt"},
	{"EXPENSE", "Home", "sofa"},
	{"EXPENSE", "Entertainment", "clapperboard"},
	{"EXPENSE", "Subscriptions", "repeat"},
	{"EXPENSE", "Personal care", "sparkles"},
	{"EXPENSE", "Pets", "paw-print"},
	{"EXPENSE", "Gifts and donations", "heart-handshake"},
	{"EXPENSE", "Other expenses", "circle-ellipsis"},
}

// BusinessEN is Business in English, in the same order with the same icons.
var BusinessEN = []Category{
	{"INCOME", "Sales", "shopping-cart"},
	{"INCOME", "Services", "handshake"},
	{"INCOME", "Interest", "percent"},
	{"INCOME", "Other income", "circle-plus"},

	{"EXPENSE", "Inventory", "package"},
	{"EXPENSE", "Suppliers", "truck"},
	{"EXPENSE", "Payroll", "users"},
	{"EXPENSE", "Rent", "building"},
	{"EXPENSE", "Utilities", "zap"},
	{"EXPENSE", "Shipping and transport", "send"},
	{"EXPENSE", "Marketing", "megaphone"},
	{"EXPENSE", "Taxes", "landmark"},
	{"EXPENSE", "Bank fees", "credit-card"},
	{"EXPENSE", "Maintenance", "wrench"},
	{"EXPENSE", "Software and subscriptions", "laptop"},
	{"EXPENSE", "Professional fees", "scale"},
	{"EXPENSE", "Other expenses", "circle-ellipsis"},
}

// For returns the defaults for a workspace type in a language ("es" or
// "en"; anything else gets Spanish).
func For(workspaceType, locale string) []Category {
	business := workspaceType == "BUSINESS"
	switch {
	case business && locale == "en":
		return BusinessEN
	case business:
		return Business
	case locale == "en":
		return PersonalEN
	default:
		return Personal
	}
}

// Icons are the icons a category may use, in the order the app's picker
// shows them. Every default's icon is in this list.
var Icons = []string{
	"tag", "utensils", "coffee", "shopping-cart", "shopping-bag", "bus", "car", "fuel", "plane",
	"house", "sofa", "zap", "droplet", "wifi", "smartphone", "heart-pulse", "pill", "graduation-cap",
	"book", "shirt", "sparkles", "dumbbell", "clapperboard", "music", "gamepad-2", "repeat",
	"paw-print", "baby", "gift", "heart-handshake", "briefcase", "store", "trending-up", "percent",
	"piggy-bank", "handshake", "package", "truck", "send", "users", "building", "megaphone",
	"landmark", "credit-card", "wrench", "laptop", "scale", "receipt", "circle-plus", "circle-ellipsis",
}

// ValidIcon reports whether icon may be used by a category.
func ValidIcon(icon string) bool {
	for _, i := range Icons {
		if i == icon {
			return true
		}
	}
	return false
}
