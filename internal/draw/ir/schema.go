package ir

// SchemaJSON returns the JSON Schema specification (Draft 7 / 2020-12 compatible)
// for SymDraw diagrams.
const SchemaJSON = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "SymDraw Diagram IR",
  "description": "Input-independent diagram model for SymDraw rendering pipeline",
  "type": "object",
  "required": ["kind"],
  "properties": {
    "version": {"type": "string"},
    "kind": {
      "type": "string",
      "enum": ["graph", "sequence", "timeline", "tree", "chart", "custom"]
    },
    "title": {"type": "string"},
    "direction": {
      "type": "string",
      "enum": ["TD", "TB", "BT", "LR", "RL"]
    },
    "theme": {"type": "string"},
    "width": {"type": "number", "minimum": 0},
    "height": {"type": "number", "minimum": 0},
    "nodes": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id"],
        "properties": {
          "id": {"type": "string", "minLength": 1},
          "label": {"type": "string"},
          "shape": {
            "type": "string",
            "enum": ["rect", "round", "circle", "cylinder", "diamond", "pill", "stadium", "subroutine", "hexagon", "asymmetric"]
          },
          "note": {"type": "string"},
          "icon": {"type": "string"},
          "width": {"type": "number", "minimum": 0},
          "height": {"type": "number", "minimum": 0},
          "x": {"type": "number"},
          "y": {"type": "number"},
          "style": {
            "type": "object",
            "properties": {
              "fill": {"type": "string"},
              "stroke": {"type": "string"},
              "stroke_width": {"type": "number", "minimum": 0},
              "text_color": {"type": "string"},
              "opacity": {"type": "number", "minimum": 0, "maximum": 1},
              "dash_array": {"type": "string"}
            }
          }
        }
      }
    },
    "edges": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["from", "to"],
        "properties": {
          "from": {"type": "string", "minLength": 1},
          "to": {"type": "string", "minLength": 1},
          "label": {"type": "string"},
          "style": {
            "type": "string",
            "enum": ["solid", "dashed", "dotted", "thick"]
          },
          "arrow": {
            "type": "string",
            "enum": ["none", "single", "double", "cross", "circle"]
          },
          "color": {"type": "string"}
        }
      }
    },
    "groups": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["members"],
        "properties": {
          "id": {"type": "string"},
          "label": {"type": "string"},
          "members": {
            "type": "array",
            "items": {"type": "string"},
            "minItems": 1
          },
          "style": {
            "type": "object",
            "properties": {
              "fill": {"type": "string"},
              "stroke": {"type": "string"},
              "stroke_width": {"type": "number", "minimum": 0}
            }
          }
        }
      }
    },
    "chart": {
      "type": "object",
      "required": ["type", "series"],
      "properties": {
        "type": {
          "type": "string",
          "enum": ["bar", "line", "pie", "scatter", "area", "donut"]
        },
        "title": {"type": "string"},
        "legend": {"type": "boolean"},
        "series": {
          "type": "array",
          "items": {
            "type": "object",
            "required": ["name", "data"],
            "properties": {
              "name": {"type": "string"},
              "color": {"type": "string"},
              "data": {
                "type": "array",
                "items": {
                  "type": "object",
                  "required": ["y"],
                  "properties": {
                    "x": {"type": "number"},
                    "y": {"type": "number"},
                    "label": {"type": "string"},
                    "color": {"type": "string"}
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`
