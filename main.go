package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
	"unsafe"

	"github.com/ichiban/prolog"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// Create a new MCP server
	s := server.NewMCPServer(
		"Prolog MCP",
		"1.0.0",
		server.WithResourceCapabilities(true, true),
		server.WithLogging(),
	)

	p := prolog.New(nil, nil)
	prologCtx := context.Background()

	query := mcp.NewTool("query", mcp.WithDescription("Query the prolog engine"),
		mcp.WithString("query", mcp.Required(), mcp.Description("The query to execute")),
	)

	exec := mcp.NewTool("exec", mcp.WithDescription("Execute a prolog program"),
		mcp.WithString("program", mcp.Required(), mcp.Description("The prolog program to execute")),
	)

	discover := mcp.NewTool("discover", mcp.WithDescription("Shows the available predicates in the prolog engine"))

	s.AddTool(query, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := request.Params.Arguments["query"].(string)

		// Create a context with a 15-second timeout derived from the handler's context
		queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel() // Ensure the cancel function is called to release resources

		// Use the time-limited context for the query
		solutions, err := p.QueryContext(queryCtx, query) // Pass queryCtx here
		if err != nil {
			// Check specifically for context deadline exceeded
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("query timed out after 15 seconds: %w", err)
			}
			return nil, fmt.Errorf("query error: %w", err)
		}
		defer solutions.Close() // Ensure resources are released

		var allSolutions []map[string]any // Slice to hold all solution maps

		// Iterate through all possible solutions
		for solutions.Next() {
			solutionMap := make(map[string]any)
			// Scan the *current* solution's bindings into the map
			if err := solutions.Scan(solutionMap); err != nil {
				// Handle potential scan errors (though less common if Next() succeeded)
				return nil, fmt.Errorf("error scanning solution: %w", err)
			}
			allSolutions = append(allSolutions, solutionMap) // Add the map for this solution
		}

		// Check for errors *after* iteration (e.g., resource limits exceeded, internal errors, timeout)
		if err := solutions.Err(); err != nil {
			// Check specifically for context deadline exceeded during iteration
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("query iteration timed out after 15 seconds: %w", err)
			}
			return nil, fmt.Errorf("error during query iteration: %w", err)
		}

		// Format the result (using fmt.Sprintf for consistency)
		// If no solutions were found, allSolutions will be an empty slice: []
		return mcp.NewToolResultText(fmt.Sprintf("%v", allSolutions)), nil
	})

	s.AddTool(exec, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		program := request.Params.Arguments["program"].(string)
		err := p.ExecContext(prologCtx, program)
		if err != nil {
			return nil, err
		}
		return mcp.NewToolResultText("Program executed successfully"), nil
	})

	s.AddTool(discover, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		predicates := new([]string)

		v := reflect.ValueOf(p).Elem()

		proceduresField := v.FieldByName("procedures")

		var accessibleProcedures reflect.Value
		var baseValue reflect.Value

		if proceduresField.IsValid() {
			baseValue = v
		} else {
			vmField := v.FieldByName("VM")
			if !vmField.IsValid() {
				return nil, fmt.Errorf("could not find 'procedures' field (tried promotion and explicit 'VM' embed)")
			}
			proceduresField = vmField.FieldByName("procedures")
			if !proceduresField.IsValid() {
				return nil, fmt.Errorf("found embedded 'VM' field, but not 'procedures' field within it")
			}
			baseValue = vmField
		}

		if !baseValue.CanAddr() {
			return nil, fmt.Errorf("internal error: base value for field is not addressable")
		}

		// Get the field description (StructField) from the base struct's type to find its offset.
		structFieldDesc, found := baseValue.Type().FieldByName("procedures")
		if !found {
			// This should theoretically not happen based on prior checks finding the Value
			return nil, fmt.Errorf("internal error: could not get StructField description for 'procedures' in type %v", baseValue.Type())
		}

		// Calculate the pointer using the Offset from the StructField description.
		fieldPtr := unsafe.Pointer(baseValue.UnsafeAddr() + structFieldDesc.Offset)

		// Create the accessible value using the Type from the original field Value.
		accessibleProcedures = reflect.NewAt(proceduresField.Type(), fieldPtr).Elem()

		if accessibleProcedures.Kind() == reflect.Map {
			iter := accessibleProcedures.MapRange()
			for iter.Next() {
				k := iter.Key()

				pkMethod := k.MethodByName("String")
				var procString string
				if pkMethod.IsValid() {
					results := pkMethod.Call(nil)
					if len(results) > 0 && results[0].Kind() == reflect.String {
						procString = results[0].String()
					} else {
						procString = fmt.Sprintf("error<key=%v, unexpected_result=%v>", k, results)
					}
				} else {
					procString = fmt.Sprintf("error<key=%v, method_not_found>", k)
				}
				*predicates = append(*predicates, procString)
			}
		} else {
			return nil, fmt.Errorf("procedures field is not a map, kind: %v", accessibleProcedures.Kind())
		}
		resultString := fmt.Sprintf("%v", *predicates)
		return mcp.NewToolResultText(resultString), nil
	})

	// Start the stdio server
	if err := server.ServeStdio(s); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}
