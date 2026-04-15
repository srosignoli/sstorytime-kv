
ifeq ($(OS),Windows_NT)
	EXE := .exe
else
	EXE :=
endif

OBJ := N4L$(EXE) searchN4L$(EXE) pathsolve$(EXE)

all: $(OBJ)

N4L$(EXE): src/N4L.go pkg/SSTorytime/SSTorytime.go
	go build -o $@ ./src/N4L.go

searchN4L$(EXE): src/searchN4L.go pkg/SSTorytime/SSTorytime.go
	go build -o $@ ./src/searchN4L.go

pathsolve$(EXE): src/pathsolve.go pkg/SSTorytime/SSTorytime.go
	go build -o $@ ./src/pathsolve.go

test: $(OBJ)
	$(MAKE) -C tests

clean:
	rm -f $(OBJ)
	rm -f *~ \#*
	$(MAKE) -C tests clean
	$(MAKE) -C examples clean
