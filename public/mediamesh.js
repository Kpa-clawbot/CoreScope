/* mediamesh.js — MediaMesh (radio2) nodes: shared by the Live and Map pages.
 * IIFE attaching window.MediaMesh.
 *
 * MediaMesh is a second LoRa profile (radio2) that Keymind Cascade repeaters,
 * Wadamesh TAK standalones and MeshCore Open TAK use for photos and voice.
 * The server adds `mediamesh: {mode, radio, source, last_seen}` to a node
 * that runs it (mode rx = listens, rxtx = listens and sends); see
 * cmd/server/mediamesh.go. Nodes without it are left exactly as they were.
 *
 * Pure functions apart from the optional window.timeAgo; works in a Node vm
 * sandbox.
 */
(function () {
  'use strict';

  var SOURCES = {
    status: 'reported by the node',
    neighbor: 'reported by a neighbour',
    heard: 'heard sending on radio2'
  };

  // The node's MediaMesh mark, or null.
  function info(node) {
    var mm = node && node.mediamesh;
    if (!mm || typeof mm !== 'object') return null;
    if (mm.mode !== 'rx' && mm.mode !== 'rxtx') return null;
    return mm;
  }

  // Extra class names for a node's map marker: a ring, and a badge filled for
  // rxtx and hollow for rx (live.css / style.css .mm-node).
  function markerClass(node) {
    var mm = info(node);
    return mm ? ' mm-node mm-node--' + mm.mode : '';
  }

  // "MHz,kHz,SF,CR" -> "911.15 MHz · 250 kHz SF7"; '' when not a radio.
  function describeRadio(radio) {
    if (typeof radio !== 'string') return '';
    var p = radio.split(',');
    if (p.length < 3) return '';
    var mhz = parseFloat(p[0]), khz = parseFloat(p[1]), sf = parseInt(p[2], 10);
    if (!(mhz > 0) || !(khz > 0) || !(sf > 0)) return '';
    var trim = function (v, d) { return String(parseFloat(v.toFixed(d))); };
    return trim(mhz, 3) + ' MHz · ' + trim(khz, 1) + ' kHz SF' + sf;
  }

  function modeLabel(mode) {
    return mode === 'rxtx' ? 'listens and sends' : 'listens';
  }

  // One plain-text line: "listens and sends · 911.15 MHz · 250 kHz SF7 —
  // reported by the node, 2h ago". Callers escape it for HTML.
  function describe(node) {
    var mm = info(node);
    if (!mm) return '';
    var parts = [modeLabel(mm.mode)];
    var r = describeRadio(mm.radio);
    if (r) parts.push(r);
    var how = SOURCES[mm.source] || '';
    var ago = (mm.last_seen && typeof window !== 'undefined' && typeof window.timeAgo === 'function')
      ? window.timeAgo(mm.last_seen) : '';
    var tail = [how, ago].filter(Boolean).join(', ');
    return parts.join(' · ') + (tail ? ' — ' + tail : '');
  }

  // Map filter: with "MediaMesh only" on, a node passes only if it runs it.
  function filterAccepts(node, onlyMediaMesh) {
    return !onlyMediaMesh || !!info(node);
  }

  var api = {
    info: info,
    markerClass: markerClass,
    describeRadio: describeRadio,
    describe: describe,
    filterAccepts: filterAccepts
  };
  if (typeof window !== 'undefined') window.MediaMesh = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})();
