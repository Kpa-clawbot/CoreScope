/**
 * Channel Color Highlighting — Storage Model (M1)
 *
 * localStorage key: 'live-channel-colors'
 * Value: JSON object mapping channel names to hex colors
 * e.g. { "#wardriving": "#ef4444", "#meshnet": "#3b82f6" }
 *
 * Only applies to GRP_TXT packets. Other types retain default styling.
 */
(function() {
  'use strict';

  var STORAGE_KEY = 'live-channel-colors';

  function _load() {
    try {
      return JSON.parse(localStorage.getItem(STORAGE_KEY)) || {};
    } catch (e) {
      return {};
    }
  }

  function _save(colors) {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(colors));
  }

  /**
   * Get the assigned color for a channel, or null if unassigned.
   * @param {string} channel - Channel name (e.g. "#test")
   * @returns {string|null} Hex color or null
   */
  function getChannelColor(channel) {
    if (!channel) return null;
    var colors = _load();
    return colors[channel] || null;
  }

  /**
   * Assign a color to a channel.
   * @param {string} channel - Channel name
   * @param {string} color - Hex color (e.g. "#ef4444")
   */
  function setChannelColor(channel, color) {
    if (!channel || !color) return;
    var colors = _load();
    colors[channel] = color;
    _save(colors);
  }

  /**
   * Remove the color assignment for a channel.
   * @param {string} channel - Channel name
   */
  function removeChannelColor(channel) {
    if (!channel) return;
    var colors = _load();
    delete colors[channel];
    _save(colors);
  }

  /**
   * Get all channel-color assignments.
   * @returns {Object} Map of channel name → hex color
   */
  function getAllChannelColors() {
    return _load();
  }

  /**
   * Compute inline style string for a feed row / table row based on channel color.
   * Returns empty string if no channel color is assigned.
   * @param {string} typeName - Packet type name (e.g. "GRP_TXT", "CHAN")
   * @param {string|null} channel - Channel name from decoded payload
   * @returns {string} Inline style string or empty
   */
  function getChannelRowStyle(typeName, channel) {
    // Only GRP_TXT / CHAN packets get channel coloring
    if (typeName !== 'GRP_TXT' && typeName !== 'CHAN') return '';
    if (!channel) return '';
    var color = getChannelColor(channel);
    if (!color) return '';
    // 4px left border + 10% opacity background tint
    return 'border-left:4px solid ' + color + ';background:' + color + '1a;';
  }

  // Export to window for use by live.js and packets.js
  window.ChannelColors = {
    get: getChannelColor,
    set: setChannelColor,
    remove: removeChannelColor,
    getAll: getAllChannelColors,
    getRowStyle: getChannelRowStyle
  };
})();
