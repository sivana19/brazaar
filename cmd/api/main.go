package main

import (
    "context"
    "log"
    "net/http"
    "os"
    "strconv"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/joho/godotenv"
    swaggerFiles "github.com/swaggo/files"
    ginSwagger "github.com/swaggo/gin-swagger"

    _ "bazaar/docs"
    "bazaar/internal/middleware"
    "bazaar/internal/order"
    "bazaar/internal/product"
    "bazaar/internal/user"
)

// @title           Bazaar E-Commerce API
// @version         1.0
// @description     High-performance Go backend with PostgreSQL connection pooling and JWT auth.
// @host            localhost:8080
// @BasePath        /
// @securityDefinitions.apikey BearerAuth
// @in              header
// @name            Authorization
// @description     Type 'Bearer ' followed by your JWT token.
func main() {
    if err := godotenv.Load(); err != nil {
        log.Println("[!] No .env file found; falling back to environment variables")
    }

    dbURL := os.Getenv("DATABASE_URL")
    if dbURL == "" {
        log.Fatal("[FATAL] DATABASE_URL is not set in environment")
    }

    jwtSecret := os.Getenv("JWT_SECRET")
    if jwtSecret == "" {
        jwtSecret = "default-secret-key-change-in-production"
    }

    poolConfig, err := pgxpool.ParseConfig(dbURL)
    if err != nil {
        log.Fatalf("[FATAL] Unable to parse DATABASE_URL: %v", err)
    }

    poolConfig.MaxConns = 15
    poolConfig.MinConns = 2
    poolConfig.MaxConnIdleTime = 5 * time.Minute

    ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
    defer cancel()

    pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
    if err != nil {
        log.Fatalf("[FATAL] Unable to connect to Neon database: %v", err)
    }
    defer pool.Close()

    if err := pool.Ping(ctx); err != nil {
        log.Fatalf("[FATAL] Database ping failed: %v", err)
    }
    log.Println("[✓] Successfully connected to Neon PostgreSQL database")

    r := gin.Default()

    // Swagger documentation route
    r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

    // Auth routes
    userRepo := user.NewRepository(pool, jwtSecret)
    authRoutes := r.Group("/auth")
    {
        authRoutes.POST("/register", func(c *gin.Context) {
            var input user.RegisterInput
            if err := c.ShouldBindJSON(&input); err != nil {
                c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
                return
            }
            u, err := userRepo.Register(c.Request.Context(), input)
            if err != nil {
                c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
                return
            }
            c.JSON(http.StatusCreated, u)
        })

        authRoutes.POST("/login", func(c *gin.Context) {
            var input user.LoginInput
            if err := c.ShouldBindJSON(&input); err != nil {
                c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
                return
            }
            token, u, err := userRepo.Login(c.Request.Context(), input)
            if err != nil {
                c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
                return
            }
            c.JSON(http.StatusOK, gin.H{
                "token": token,
                "user":  u,
            })
        })
    }

    // Product routes
    productRepo := product.NewRepository(pool)
    productRoutes := r.Group("/products")
    {
        productRoutes.GET("", func(c *gin.Context) {
            page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
            limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
            if page < 1 {
                page = 1
            }
            if limit < 1 || limit > 100 {
                limit = 10
            }
            offset := (page - 1) * limit

            rows, err := pool.Query(c.Request.Context(), `
                SELECT id, name, description, price_cents, stock, created_at, updated_at
                FROM products
                ORDER BY id ASC
                LIMIT $1 OFFSET $2
            `, limit, offset)
            if err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
                return
            }
            defer rows.Close()

            var products []map[string]interface{}
            for rows.Next() {
                var id int64
                var name, description string
                var priceCents int64
                var stock int
                var createdAt, updatedAt time.Time
                if err := rows.Scan(&id, &name, &description, &priceCents, &stock, &createdAt, &updatedAt); err == nil {
                    products = append(products, map[string]interface{}{
                        "id":          id,
                        "name":        name,
                        "description": description,
                        "price_cents": priceCents,
                        "stock":       stock,
                        "created_at":  createdAt,
                        "updated_at":  updatedAt,
                    })
                }
            }
            c.JSON(http.StatusOK, products)
        })

        productRoutes.GET("/:id", func(c *gin.Context) {
            id, err := strconv.ParseInt(c.Param("id"), 10, 64)
            if err != nil {
                c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
                return
            }

            var name, description string
            var priceCents int64
            var stock int
            var createdAt, updatedAt time.Time

            err = pool.QueryRow(c.Request.Context(), `
                SELECT name, description, price_cents, stock, created_at, updated_at
                FROM products
                WHERE id = $1
            `, id).Scan(&name, &description, &priceCents, &stock, &createdAt, &updatedAt)
            if err != nil {
                c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
                return
            }

            c.JSON(http.StatusOK, gin.H{
                "id":          id,
                "name":        name,
                "description": description,
                "price_cents": priceCents,
                "stock":       stock,
                "created_at":  createdAt,
                "updated_at":  updatedAt,
            })
        })

        productRoutes.DELETE("/:id", middleware.AuthMiddleware(), func(c *gin.Context) {
            id, err := strconv.Atoi(c.Param("id"))
            if err != nil {
                c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
                return
            }
            ok, err := productRepo.Delete(c.Request.Context(), id)
            if err != nil || !ok {
                c.JSON(http.StatusNotFound, gin.H{"error": "failed to delete product"})
                return
            }
            c.JSON(http.StatusOK, gin.H{"message": "product deleted"})
        })
    }

    // Order routes
    orderHandler := order.NewHandler(pool)
    orderRoutes := r.Group("/orders")
    orderRoutes.Use(middleware.AuthMiddleware())
    {
        orderRoutes.POST("", orderHandler.CreateOrder)
        orderRoutes.GET("", orderHandler.ListOrders)
        orderRoutes.GET("/:id", orderHandler.GetOrder)
        orderRoutes.PATCH("/:id/status", orderHandler.UpdateOrderStatus)
    }

    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }
    log.Printf("[*] Bazaar API starting on port %s", port)
    if err := r.Run(":" + port); err != nil {
        log.Fatalf("[FATAL] Server failed: %v", err)
    }
}
