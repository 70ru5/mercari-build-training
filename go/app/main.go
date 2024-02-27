package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"

	"database/sql"

	_ "github.com/mattn/go-sqlite3"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"
)

const (
	ImgDir = "images"
	DBPath = "../db/mercari.sqlite3"
)

type Response struct {
	Message string `json:"message"`
}

type Item struct {
	Name      string `json:"name"`
	Category  string `json:"category"`
	ImageName string `json:"image_name,omitempty"`
}

type Items struct {
	Items []Item `json:"items"`
}

// scanRowsToItems is a method for type Items.
// It scans *sql.rows and turns it into type Items that has item name and category name.
func (items *Items) ScanRowsToItems(rows *sql.Rows) error {
	for rows.Next() {
		var item Item
		err := rows.Scan(&item.Name, &item.Category)
		if err != nil {
			return err
		}
		items.Items = append(items.Items, item)
	}
	return nil
}

type ServerImpl struct {
	DB *sql.DB
}

func root(c echo.Context) error {
	res := Response{Message: "Hello, world!!"}
	return c.JSON(http.StatusOK, res)
}

// addItem processes form data and saves item information.
func (db *ServerImpl) addItem(c echo.Context) error {
	// Get form data
	name := c.FormValue("name")
	category := c.FormValue("category")
	image, err := c.FormFile("image")
	if err != nil {

		c.Logger().Errorf("Error while retrieving image: %w", err)
		res := Response{Message: "Error while retrieving image"}
		return echo.NewHTTPError(http.StatusBadRequest, res)
	}

	c.Logger().Infof("Receive item: %s, Category: %s", name, category)

	fileName, err := saveImage(image)
	if err != nil {
		c.Logger().Errorf("Error while hashing and saving image: %w", err)
		res := Response{Message: "Error while hashing and saving image"}
		return echo.NewHTTPError(http.StatusInternalServerError, res)
	}

	if err := db.saveItem(name, category, fileName); err != nil {
		c.Logger().Errorf("Error while saving item information: %w", err)
		res := Response{Message: "Error while saving item information"}
		return echo.NewHTTPError(http.StatusInternalServerError, res)
	}

	message := fmt.Sprintf("item received: %s", name)
	res := Response{Message: message}

	return c.JSON(http.StatusOK, res)
}

// saveItem writes the item information into the database.
func (db *ServerImpl) saveItem(name, category, fileName string) error {

	// Transaction starts.
	tx, err := db.DB.Begin()
	if err != nil {
		err = fmt.Errorf("error while beginning transaction: %w", err)
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			log.Printf("error while rolling back transaction: %w", err)
		}
	}()

	categoryId, err := checkCategoryId(category, tx)
	if err != nil {
		err = fmt.Errorf("error while searching for category id: %w", err)
		return err
	}

	const insertItem = "INSERT INTO items (name, image_name, category_id) VALUES (?, ?, ?)"
	_, err = tx.Exec(insertItem, name, fileName, categoryId)
	if err != nil {
		err = fmt.Errorf("error while adding a new item: %w", err)
		return err
	} else {
		if err := tx.Commit(); err != nil {
			err = fmt.Errorf("error while commiting transaction: %w", err)
			return err
		}
	}

	return nil
}

// checkCategoryId look for category ID, if it doesn't exist, register a new category.
func checkCategoryId(category string, tx *sql.Tx) (int, error) {
	var categoryId int

	const findCategoryId = "SELECT id FROM categories WHERE name = ?"
	rows := tx.QueryRow(findCategoryId, category)

	err := rows.Scan(&categoryId)
	if err == sql.ErrNoRows {
		const makeNewCategory = "INSERT INTO categories (name) values (?)"
		result, err := tx.Exec(makeNewCategory, category)
		if err != nil {
			log.Errorf("error while inserting new category: %w", err)
			return 0, err
		}

		newId, err := result.LastInsertId()
		if err != nil {
			log.Errorf("error while getting new category ID: %w", err)
			return 0, err
		}
		categoryId = int(newId)

	} else if err != nil {
		log.Errorf("error while scanning category ID: %w", err)
		return 0, err
	}

	return categoryId, nil
}

// saveImage hashes the image, saves it, and returns its file name.
func saveImage(image *multipart.FileHeader) (string, error) {

	img, err := image.Open()
	if err != nil {
		return "", err
	}
	source, err := io.ReadAll(img)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(source)

	err = os.MkdirAll("./images", 0750)
	if err != nil {
		return "", err
	}

	fileName := fmt.Sprintf("%x.jpg", hash)
	imagePath := fmt.Sprintf("./images/%s", fileName)

	_, err = os.Create(imagePath)
	if err != nil {
		return "", err
	}

	err = os.WriteFile(imagePath, source, 0644)
	if err != nil {
		return "", err
	}

	return fileName, err
}

// getItems gets all the item information.
func (db *ServerImpl) getItems(c echo.Context) error {

	items, err := db.readItems()
	if err != nil {
		c.Logger().Errorf("Error while reading item information: %w", err)
		res := Response{Message: "Error while reading item information"}
		return echo.NewHTTPError(http.StatusInternalServerError, res)
	}

	return c.JSON(http.StatusOK, items)
}

// readItems reads database and returns all the item information.
func (db *ServerImpl) readItems() (Items, error) {

	const selectAllItems = "SELECT items.name, categories.name FROM items JOIN categories ON items.category_id = categories.id"
	rows, err := db.DB.Query(selectAllItems)
	if err != nil {
		return Items{}, err
	}

	items := new(Items)
	err = items.ScanRowsToItems(rows)
	if err != nil {
		return Items{}, err
	}

	return *items, nil
}

func (db *ServerImpl) searchItems(c echo.Context) error {
	keyword := c.QueryParam("keyword")
	key := "%" + keyword + "%"

	const searchWithKey = "SELECT items.name, categories.name FROM items JOIN categories ON items.category_id = categories.id WHERE items.name LIKE ?"
	rows, err := db.DB.Query(searchWithKey, key)
	if err != nil {
		c.Logger().Errorf("Error while searching with keyword: %w", err)
		res := Response{Message: "Error while searching with keyword"}
		return echo.NewHTTPError(http.StatusInternalServerError, res)
	}

	resultItems := new(Items)
	err = resultItems.ScanRowsToItems(rows)
	if err != nil {
		c.Logger().Errorf("Error while scanning rows to items: %w", err)
		res := Response{Message: "Error while scanning rows to items"}
		return echo.NewHTTPError(http.StatusInternalServerError, res)
	}

	return c.JSON(http.StatusOK, resultItems)
}

// getImg gets the designated image by file name.
func getImg(c echo.Context) error {
	// Create image path
	imgPath := path.Join(ImgDir, c.Param("imageFilename"))

	if !strings.HasSuffix(imgPath, ".jpg") {
		res := Response{Message: "Image path does not end with .jpg"}
		return c.JSON(http.StatusBadRequest, res)
	}
	if _, err := os.Stat(imgPath); err != nil {
		c.Logger().Debugf("Image not found: %s", imgPath)
		imgPath = path.Join(ImgDir, "default.jpg")
	}
	return c.File(imgPath)
}

// getInfo gets detailed information of the designeted item by id.
func (db *ServerImpl) getInfoById(c echo.Context) error {
	itemId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.Logger().Errorf("Invalid ID: %w", err)
		res := Response{Message: "Invalid ID"}
		return echo.NewHTTPError(http.StatusInternalServerError, res)
	}

	var item Item
	selectById := "SELECT items.name, categories.name, items.image_name FROM items JOIN categories ON items.category_id = categories.id WHERE items.id = ?"
	rows := db.DB.QueryRow(selectById, itemId)
	err = rows.Scan(&item.Name, &item.Category, &item.ImageName)
	if err != nil {
		c.Logger().Errorf("Error while searching item with ID: %w", err)
		res := Response{Message: "Error while searching item with ID"}
		return echo.NewHTTPError(http.StatusInternalServerError, res)
	}

	return c.JSON(http.StatusOK, item)
}

// 　connectDB opens database connection.
func connectDB(dbPath string) (*sql.DB, error) {
	dbCon, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	return dbCon, nil
}

func main() {
	e := echo.New()

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Logger.SetLevel(log.INFO)

	frontURL := os.Getenv("FRONT_URL")
	if frontURL == "" {
		frontURL = "http://localhost:3000"
	}
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{frontURL},
		AllowMethods: []string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete},
	}))

	// connect to database
	dbCon, err := connectDB(DBPath)
	if err != nil {
		log.Errorf("Error whole connecting to database: %w", err)
	}
	defer dbCon.Close()

	db := ServerImpl{DB: dbCon}

	// Routes
	e.GET("/", root)
	e.POST("/items", db.addItem)
	e.GET("/items", db.getItems)
	e.GET("/image/:imageFilename", getImg)
	e.GET("/items/:id", db.getInfoById)
	e.GET("/search", db.searchItems)

	// Start server
	e.Logger.Fatal(e.Start(":9000"))
}
